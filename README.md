gttp: http for gophers
----------------------

This program is a minimal clone of https://github.com/httpie/httpie, "A
modern, user-friendly command-line HTTP client for the API era."

The reason for writing my own is that python's 100ms+ startup time was really
starting to bug me.

The goal is not to write a full command-line http client, but rather to make a
tool that makes it easier to interactively poke at HTTP and JSON services.

Headers, query parameters and form data are all specified on the command line,
using a different separator depending on the type of key-value pair wanted.
Headers use `:`, query params `==`, and form-data uses `=`.  For raw JSON data,
use `:=`.  Raw JSON allows complex types to be sent and also doesn't coerce
booleans and numbers to strings.

By default, the parameters are sent as JSON unless `-f` (form-data) is passed,
in which case the content-type is set to "application/x-www-form-urlencoded".

Some examples:

    gttp httpbin.org/get Custom-Header:"header value" queryparam==value

    gttp -f httpbin.org/post formdata1=value1 formdata2=value2

    gttp POST httpbin.org/post jsondata1=1 jsondata2:=2 jsondata3:='[1,2,{"complex":["json", "data"]}]'

    gttp -auth="foouser:foopass" httpbin.org/basic-auth/foouser/foopass 


This tool certainly isn't finished, but I've switched over to using it for my
needs (which are admittedly minimal.)

Pull requests gladly accepted.
