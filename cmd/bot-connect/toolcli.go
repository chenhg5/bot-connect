package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// toolCLI implements `bot-connect tool [name] [json-args]` for brains that
// call tools from a shell instead of MCP. It talks to the running
// bot-connect through BOT_CONNECT_API, which is set for each brain turn.
func toolCLI(args []string) int {
	api := os.Getenv("BOT_CONNECT_API")
	if api == "" {
		fmt.Fprintln(os.Stderr, "BOT_CONNECT_API is not set (this command only works inside a bot-connect brain turn)")
		return 2
	}
	hc := &http.Client{Timeout: 60 * time.Second}
	if len(args) == 0 || args[0] == "list" || args[0] == "-h" || args[0] == "--help" {
		resp, err := hc.Get(api + "/tools")
		return printResp(resp, err, true)
	}
	// Args come from the command line; "-" reads them from stdin. Never read
	// stdin implicitly: a brain that got its prompt on stdin shares it with us.
	body := "{}"
	if len(args) > 1 {
		body = strings.Join(args[1:], " ")
	}
	if body == "-" {
		b, _ := io.ReadAll(os.Stdin)
		body = string(bytes.TrimSpace(b))
	}
	resp, err := hc.Post(api+"/tools/"+args[0], "application/json", strings.NewReader(body))
	return printResp(resp, err, false)
}

func printResp(resp *http.Response, err error, raw bool) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "http %d: %s", resp.StatusCode, b)
		return 1
	}
	if raw {
		var v any
		_ = json.Unmarshal(b, &v)
		out, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(out))
		return 0
	}
	var r struct {
		OK     bool   `json:"ok"`
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(b, &r)
	if !r.OK {
		fmt.Fprintln(os.Stderr, "error:", r.Error)
		return 1
	}
	fmt.Println(r.Result)
	return 0
}
