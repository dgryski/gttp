package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/daviddengcn/go-colortext"
)

/*
TODO:
    allow setting content-type for uploaded files
    disable json formatting if output is not terminal (isatty)
    read password from terminal if no password given ( https://github.com/howeyc/gopass )
*/

type kvtype int

const (
	kvpUnknown kvtype = iota
	kvpHeader
	kvpQuery
	kvpBody
	kvpJSON
	kvpFile
)

type kvpairs struct {
	headers map[string]string
	query   map[string][]string
	body    map[string][]string
	js      map[string]string
	file    map[string]string // filename, not content
}

func unescape(s string) string {
	u := make([]rune, 0, len(s))
	var escape bool
	for _, c := range s {
		if escape {
			u = append(u, c)
			escape = false
			continue
		}
		if c == '\\' {
			escape = true
			continue
		}
		u = append(u, c)
	}

	return string(u)
}

func parseKeyValue(keyvalue string) (kvtype, string, string) {

	k := make([]rune, 0, len(keyvalue))
	var escape bool
	for i, c := range keyvalue {
		if escape {
			k = append(k, c)
			escape = false
			continue
		}
		if c == '\\' {
			escape = true
			continue
		}
		// TODO(dgryski): make sure we don't overstep the array
		if c == ':' {
			if i+1 < len(keyvalue) && keyvalue[i+1] == '=' {
				// found ':=', a raw json param
				return kvpJSON, string(k), unescape(keyvalue[i+2:])
			}
			// found ':' , a header
			return kvpHeader, string(k), unescape(keyvalue[i+1:])
		} else if c == '=' {
			if i+1 < len(keyvalue) && keyvalue[i+1] == '=' {
				// found '==', a query param
				return kvpQuery, string(k), unescape(keyvalue[i+2:])
			}
			// found '=' , a form value
			return kvpBody, string(k), unescape(keyvalue[i+1:])
		} else if c == '@' {
			return kvpFile, string(k), unescape(keyvalue[i+1:])
		}
		k = append(k, c)
	}

	return kvpUnknown, "", ""
}

func parseArgs(args []string) (*kvpairs, error) {

	kvp := kvpairs{
		headers: make(map[string]string),
		query:   make(map[string][]string),
		js:      make(map[string]string),
		body:    make(map[string][]string),
		file:    make(map[string]string),
	}

	for _, arg := range args {

		t, k, v := parseKeyValue(arg)

		switch t {

		case kvpUnknown:
			return nil, errors.New("bad key/value: " + arg)

		case kvpHeader:
			kvp.headers[k] = v

		case kvpQuery:
			vs := kvp.query[k]
			kvp.query[k] = append(vs, v)

		case kvpBody:
			vs := kvp.query[k]
			kvp.body[k] = append(vs, v)

		case kvpJSON:
			kvp.js[k] = v

		case kvpFile:
			kvp.file[k] = v
		}
	}

	return &kvp, nil
}

func addValues(values url.Values, key string, vals interface{}) {

	switch val := vals.(type) {
	case bool:
		if val {
			values.Add(key, "true")
		} else {
			values.Add(key, "false")
		}
	case string:
		values.Add(key, val)
	case float64:
		values.Add(key, fmt.Sprintf("%g", val))
	case map[string]interface{}:
		for k := range val {
			addValues(values, key, k)
		}
	case []interface{}:
		for _, v := range val {
			addValues(values, key, v)
		}
	default:
		log.Println("unknown type: ", reflect.TypeOf(val))
	}
}

func main() {

	postform := flag.Bool("f", false, "post form")
	onlyHeaders := flag.Bool("headers", false, "only show headers")
	onlyBody := flag.Bool("body", false, "only show body")
	verbose := flag.Bool("v", false, "verbose")
	auth := flag.String("auth", "", "username:password")
	color := flag.Bool("color", true, "use color")
	noFormatting := flag.Bool("n", false, "no formatting/colour")
	rawOutput := flag.Bool("raw", false, "raw output (no headers/formatting/color)")
	useMultipart := flag.Bool("m", true, "use multipart if uploading files")
	timeout := flag.Duration("t", 0, "timeout (default none)")
	insecure := flag.Bool("k", false, "allow insecure TLS")
	useEnv := flag.Bool("e", true, "use proxies from environment")

	flag.Parse()

	if *noFormatting {
		*color = false
	}

	if *rawOutput {
		*onlyHeaders = false
		*onlyBody = true
		*color = false
		*noFormatting = true
	}

	if flag.NArg() == 0 {
		flag.Usage()
		return
	}

	if *timeout != 0 {
		http.DefaultClient.Timeout = *timeout
	}

	if *insecure {
		http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	if !*useEnv {
		http.DefaultTransport.(*http.Transport).Proxy = nil
	}

	args := flag.Args()

	method := "GET"
	methodProvided := false
	if *postform {
		methodProvided = true
		method = "POST"
	}

	switch args[0] {
	case "GET", "HEAD", "POST", "PUT", "DELETE", "PURGE", "TRACE", "OPTIONS", "CONNECT", "PATCH":
		methodProvided = true
		method = args[0]
		args = args[1:]
	}

	// add http:// if we need it
	if !strings.HasPrefix(args[0], "http://") && !strings.HasPrefix(args[0], "https://") {
		args[0] = "http://" + args[0]
	}
	u := args[0]
	args = args[1:]

	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		log.Fatal("error creating request object: ", err)
	}

	if *auth != "" {
		s := strings.SplitN(*auth, ":", 2)
		req.SetBasicAuth(s[0], s[1])
	}

	kvp, err := parseArgs(args)
	if err != nil {
		log.Fatal(err)
	}

	var postFiles bool
	rawBodyFilename := "" // name of file for raw body
	bodyparams := make(map[string]interface{})

	// update the raw query if we have any new parameters
	if len(kvp.query) > 0 {
		queryparams := req.URL.Query()
		for k, vs := range kvp.query {
			for _, v := range vs {
				queryparams.Add(k, v)
			}
		}
		req.URL.RawQuery = queryparams.Encode()
	}

	for k, v := range kvp.body {
		if len(v) == 1 {
			bodyparams[k] = v[0]
		} else {
			bodyparams[k] = v
		}
	}

	for k, v := range kvp.js {
		var vint interface{}
		if err = json.Unmarshal([]byte(v), &vint); err != nil {
			log.Fatal("invalid json: ", v)
		}
		bodyparams[k] = vint
	}

	// if we have at least one file, maybe upload with multipart
	postFiles = len(kvp.file) > 0

	for k, v := range kvp.file {
		if k == "-" {
			rawBodyFilename = v
			// but we're no longer posting files
			postFiles = false
		}
	}

	// assemble the body

	var body []byte

	if rawBodyFilename != "" {
		if len(kvp.file) > 1 {
			log.Fatal("only one input file allowed when setting raw body")
		}

		if len(bodyparams) > 0 {
			log.Println("extra body parameters ignored when setting raw body")
		}

		var file *os.File
		if file, err = os.Open(rawBodyFilename); err != nil {
			log.Fatal("unable to open file for body: ", err)
		}
		defer file.Close()

		body, err = ioutil.ReadAll(file)
		if err != nil {
			log.Fatal("error reading body contents: ", err)
		}

		req.Header.Add("Content-Type", "application/octet-stream")

	} else if postFiles && *useMultipart {

		// we have at least one file name
		buf := &bytes.Buffer{}

		// write the files
		writer := multipart.NewWriter(buf)
		for k, v := range kvp.file {
			var part io.Writer
			if part, err = writer.CreateFormFile(k, filepath.Base(v)); err != nil {
				log.Fatal("unable to create form file: ", err)
			}
			var file *os.File
			if file, err = os.Open(v); err != nil {
				log.Fatal("unable to open file: ", err)
			}
			defer file.Close()
			if _, err = io.Copy(part, file); err != nil {
				log.Fatal("unable to write file: ", err)
			}
		}

		// construct the extra body parameters
		values := url.Values{}
		for k, v := range bodyparams {
			addValues(values, k, v)
		}

		// and write them into the body
		for k, v := range values {
			for _, vv := range v {
				writer.WriteField(k, vv)
			}
		}

		writer.Close()

		body = buf.Bytes()
		req.Header.Add("Content-Type", writer.FormDataContentType())

	} else if len(bodyparams) > 0 || len(kvp.file) > 0 {

		// add our files as body values
		for k, v := range kvp.file {
			var file *os.File
			if file, err = os.Open(v); err != nil {
				log.Fatal("unable to open file for body: ", err)
			}
			defer file.Close()

			var val []byte
			if val, err = ioutil.ReadAll(file); err != nil {
				log.Fatal("error reading body contents: ", err)
			}
			// string so that we get file contents and not base64 encoded contents
			bodyparams[k] = string(val)
		}

		if *postform {
			values := url.Values{}
			for k, v := range bodyparams {
				addValues(values, k, v)
			}
			body = []byte(values.Encode())
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			body, _ = json.Marshal(bodyparams)
			req.Header.Set("Content-Type", "application/json")
		}
	}

	if body != nil {
		req.Body = ioutil.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Length", strconv.Itoa(len(body)))
		if !methodProvided {
			req.Method = "POST"
		}
	}

	defaultHeaders := map[string]string{
		"User-Agent": "gttp http for gophers",
		"Accept":     "*/*",
		"Host":       req.URL.Host,
	}

	for k, v := range defaultHeaders {
		req.Header.Set(k, v)
	}

	for k, v := range kvp.headers {
		req.Header.Set(k, v)
	}

	if *verbose {
		printRequestHeaders(*color, req)
		os.Stdout.Write(body)
		os.Stdout.Write([]byte{'\n', '\n'})
	}

	response, err := http.DefaultClient.Do(req)

	if err != nil {
		log.Fatal("error during fetch:", err)
	}

	if !*onlyBody {
		printResponseHeaders(*color, response)
	}

	if !*onlyHeaders {
		body, _ = ioutil.ReadAll(response.Body)
		response.Body.Close()

		if *rawOutput {
			os.Stdout.Write(body)
		} else if *noFormatting {

			if bytes.IndexByte(body, 0) != -1 {
				os.Stdout.Write([]byte(msgNoBinaryToTerminal))
			} else {
				os.Stdout.Write(body)
			}

		} else {

			// maybe do some formatting

			switch {

			case strings.HasPrefix(response.Header.Get("Content-type"), "application/json"):
				var j interface{}
				d := json.NewDecoder(bytes.NewReader(body))
				d.UseNumber()
				d.Decode(&j)
				if *color {
					printJSON(1, j, false)
				} else {
					body, _ = json.MarshalIndent(j, "", "    ")
					os.Stdout.Write(body)
				}

			case strings.HasPrefix(response.Header.Get("Content-type"), "text/"):
				os.Stdout.Write(body)

			case bytes.IndexByte(body, 0) != -1:
				// at least one 0 byte, assume it's binary data :/
				// silly, but it's the same heuristic as httpie
				os.Stdout.Write([]byte(msgNoBinaryToTerminal))

			default:
				os.Stdout.Write(body)
			}

			// formatted output ends with two newlines
			os.Stdout.Write([]byte{'\n', '\n'})
		}
	}

	if response.StatusCode >= 400 {
		os.Exit(response.StatusCode - 399)
	}
}

func printJSON(depth int, val interface{}, isKey bool) {

	switch v := val.(type) {
	case nil:
		ct.ChangeColor(ct.Blue, false, ct.None, false)
		fmt.Print("null")
		ct.ResetColor()
	case bool:
		ct.ChangeColor(ct.Blue, false, ct.None, false)
		if v {
			fmt.Print("true")
		} else {
			fmt.Print("false")
		}
		ct.ResetColor()
	case string:
		if isKey {
			ct.ChangeColor(ct.Blue, true, ct.None, false)
		} else {
			ct.ChangeColor(ct.Yellow, false, ct.None, false)
		}
		fmt.Print(strconv.Quote(v))
		ct.ResetColor()
	case json.Number:
		ct.ChangeColor(ct.Blue, false, ct.None, false)
		fmt.Print(v)
		ct.ResetColor()
	case map[string]interface{}:

		if len(v) == 0 {
			fmt.Print("{}")
			break
		}

		var keys []string

		for h := range v {
			keys = append(keys, h)
		}

		sort.Strings(keys)

		fmt.Println("{")
		needNL := false
		for _, key := range keys {
			if needNL {
				fmt.Print(",\n")
			}
			needNL = true
			for i := 0; i < depth; i++ {
				fmt.Print("    ")
			}

			printJSON(depth+1, key, true)
			fmt.Print(": ")
			printJSON(depth+1, v[key], false)
		}
		fmt.Println("")

		for i := 0; i < depth-1; i++ {
			fmt.Print("    ")
		}
		fmt.Print("}")

	case []interface{}:

		if len(v) == 0 {
			fmt.Print("[]")
			break
		}

		fmt.Println("[")
		needNL := false
		for _, e := range v {
			if needNL {
				fmt.Print(",\n")
			}
			needNL = true
			for i := 0; i < depth; i++ {
				fmt.Print("    ")
			}

			printJSON(depth+1, e, false)
		}
		fmt.Println("")

		for i := 0; i < depth-1; i++ {
			fmt.Print("    ")
		}
		fmt.Print("]")
	default:
		fmt.Println("unknown type:", reflect.TypeOf(v))
	}
}

func printRequestHeaders(useColor bool, request *http.Request) {

	u := request.URL.Path
	if u == "" {
		u = "/"
	}

	if request.URL.RawQuery != "" {
		u += "?" + request.URL.RawQuery
	}

	if useColor {
		ct.ChangeColor(ct.Green, false, ct.None, false)
		fmt.Printf("%s", request.Method)
		ct.ChangeColor(ct.Cyan, false, ct.None, false)
		fmt.Printf(" %s", u)
		ct.ChangeColor(ct.Blue, false, ct.None, false)
		fmt.Printf(" %s", request.Proto)
	} else {
		fmt.Printf("%s %s %s", request.Method, u, request.Proto)
	}

	fmt.Println()
	printHeaders(useColor, request.Header)
	fmt.Println()
}

func printResponseHeaders(useColor bool, response *http.Response) {

	if useColor {
		ct.ChangeColor(ct.Blue, false, ct.None, false)
		fmt.Printf("%s %s", response.Proto, response.Status[:3])
		ct.ChangeColor(ct.Cyan, false, ct.None, false)
		fmt.Printf("%s", response.Status[3:])
	} else {
		fmt.Printf("%s %s", response.Proto, response.Status)
	}

	fmt.Println()
	printHeaders(useColor, response.Header)
	fmt.Println()
}

func printHeaders(useColor bool, headers http.Header) {

	var keys []string

	for h := range headers {
		keys = append(keys, h)
	}

	sort.Strings(keys)

	if useColor {
		for _, k := range keys {
			ct.ChangeColor(ct.Cyan, false, ct.None, false)
			fmt.Printf("%s", k)
			ct.ChangeColor(ct.Black, false, ct.None, false)
			ct.ResetColor()
			fmt.Printf(": ")
			ct.ChangeColor(ct.Yellow, false, ct.None, false)
			fmt.Printf("%s", headers[k][0])
			ct.ResetColor()
			fmt.Println()
		}

	} else {
		for _, k := range keys {
			fmt.Printf("%s: %s\n", k, headers[k][0])
		}
	}
}

const msgNoBinaryToTerminal = "\n\n" +
	"+-----------------------------------------+\n" +
	"| NOTE: binary data not shown in terminal |\n" +
	"+-----------------------------------------+"
