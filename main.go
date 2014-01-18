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
	"net/http/httputil"
	"net/url"
	"os"
	"reflect"
	"strings"
)

/*
TODO:
    @/path/to/file (+ setting Content-Type)
    bail as binary data as soon as '\0' shows up (wrap response.Body with 'non-zero-Reader') if output is terminal
    disable json formatting if output is not terminal
    flag to disable json formatting?
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

	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		return
	}

	args := flag.Args()

	method := "GET"
	if *postform {
		method = "POST"
	}

	if args[0] == "GET" || args[0] == "POST" || args[0] == "PUT" || args[0] == "DELETE" {
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
		"User-Agent":      "gttp http for gophers",
		"Accept-Encoding": "gzip, deflate, compress",
		"Accept":          "*/*",
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
		// TODO(dgryski): write my own here?
		dump, _ := httputil.DumpRequest(req, true)
		os.Stdout.Write(dump)
		os.Stdout.Write([]byte{'\n', '\n'})
	}

	response, err := http.DefaultClient.Do(req)

	if err != nil {
		log.Fatal("error during fetch: ", err)
	}

	if !*onlyBody {

		body, _ = ioutil.ReadAll(response.Body)
		response.Body.Close()

		dump, _ := httputil.DumpResponse(response, false)
		os.Stdout.Write(dump)
		os.Stdout.Write([]byte{'\n'})

	}

	if !*onlyHeaders {

		if strings.HasPrefix(response.Header.Get("Content-type"), "application/json") {
			var j interface{}
			json.Unmarshal(body, &j)
			body, _ = json.MarshalIndent(j, "", "    ")
		}

		os.Stdout.Write(body)
		os.Stdout.Write([]byte{'\n'})
	}
}
