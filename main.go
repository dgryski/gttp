package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/daviddengcn/go-colortext"
)

/*
TODO:
    @/path/to/file (+ setting Content-Type)
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
)

type kvpairs struct {
	headers map[string]string
	query   map[string]string
	body    map[string]string
	js      map[string]string
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
			if keyvalue[i+1] == '=' {
				// found ':=', a raw json param
				return kvpJSON, string(k), unescape(keyvalue[i+2:])
			} else {
				// found ':' , a header
				return kvpHeader, string(k), unescape(keyvalue[i+1:])
			}
		} else if c == '=' {
			if keyvalue[i+1] == '=' {
				// found '==', a query param
				return kvpQuery, string(k), unescape(keyvalue[i+2:])
			} else {
				// found '=' , a form value
				return kvpBody, string(k), unescape(keyvalue[i+1:])
			}
		}
		k = append(k, c)
	}

	return kvpUnknown, "", ""
}

func parseArgs(args []string) (*kvpairs, error) {
	if len(args) == 0 {
		return nil, nil
	}

	kvp := kvpairs{
		headers: make(map[string]string),
		query:   make(map[string]string),
		js:      make(map[string]string),
		body:    make(map[string]string),
	}

	for _, arg := range args {

		t, k, v := parseKeyValue(arg)

		switch t {

		case kvpUnknown:
			return nil, errors.New("bad key/value: " + arg)

		case kvpHeader:
			kvp.headers[k] = v

		case kvpQuery:
			kvp.query[k] = v

		case kvpBody:
			kvp.body[k] = v

		case kvpJSON:
			kvp.js[k] = v
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
		for k, _ := range val {
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

	args := flag.Args()

	method := "GET"
	if *postform {
		method = "POST"
	}

	switch args[0] {
	case "GET", "HEAD", "POST", "PUT", "DELETE", "PURGE":
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

	bodyparams := make(map[string]interface{})
	if kvp != nil {

		if kvp.query != nil {
			queryparams := req.URL.Query()
			for k, v := range kvp.query {
				queryparams.Add(k, v)
			}
			req.URL.RawQuery = queryparams.Encode()
		}

		for k, v := range kvp.body {
			bodyparams[k] = v
		}

		for k, v := range kvp.js {
			var vint interface{}
			err := json.Unmarshal([]byte(v), &vint)
			if err != nil {
				log.Fatalf("invalid json: ", v)
			}
			bodyparams[k] = vint
		}
	}

	var body []byte

	if len(bodyparams) > 0 {
		if *postform {
			values := url.Values{}
			for k, v := range bodyparams {
				addValues(values, k, v)
			}
			body = []byte(values.Encode())
		} else {
			body, _ = json.MarshalIndent(bodyparams, "", "    ")
		}
		req.Body = ioutil.NopCloser(bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}

	if *postform {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	defaultHeaders := map[string]string{
		"User-Agent": "gttp http for gophers",
		"Accept":     "*/*",
		"Host":       req.URL.Host,
	}

	for k, v := range defaultHeaders {
		req.Header.Set(k, v)
	}

	if kvp != nil {
		for k, v := range kvp.headers {
			req.Header.Set(k, v)
		}
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
				json.Unmarshal(body, &j)
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
	case float64:
		ct.ChangeColor(ct.Blue, false, ct.None, false)
		fmt.Printf("%g", v)
		ct.ResetColor()
	case map[string]interface{}:

		if len(v) == 0 {
			fmt.Print("{}")
			break
		}

		var keys []string

		for h, _ := range v {
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

	for h, _ := range headers {
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
