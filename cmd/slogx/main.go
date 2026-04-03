package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

const defaultHost = "http://localhost:8080/debug/log"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		printUsage(out)
		return nil
	}

	global := flag.NewFlagSet("slogx", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	host := global.String("host", defaultHost, "Base URL where slogx HTTP handler is mounted")

	if err := global.Parse(args); err != nil {
		return err
	}

	remaining := global.Args()
	if len(remaining) == 0 {
		printUsage(out)
		return nil
	}

	client := &apiClient{
		baseURL: strings.TrimSuffix(*host, "/"),
		http:    http.DefaultClient,
	}

	command := remaining[0]
	subArgs := remaining[1:]

	switch command {
	case "level":
		return runLevel(client, subArgs, out)
	case "enable":
		return runEnable(client, subArgs, out)
	case "trace-id":
		return runTraceID(client, subArgs, out)
	case "help", "-h", "--help":
		printUsage(out)
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func runLevel(client *apiClient, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("level requires one of: get, set, delete")
	}

	switch args[0] {
	case "get":
		body, err := client.request(http.MethodGet, "/level", nil)
		if err != nil {
			return err
		}
		_, err = out.Write(body)
		return err

	case "set":
		fs := flag.NewFlagSet("level set", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		pathFlag := fs.String("path", "", "Path prefix to set (or default)")
		level := fs.String("level", "", "Log level (DEBUG, INFO, WARN, ERROR)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *level == "" {
			return errors.New("level set requires --level")
		}

		q := url.Values{}
		if *pathFlag != "" {
			q.Set("path", *pathFlag)
		} else {
			q.Set("path", "default")
		}
		q.Set("level", strings.ToUpper(*level))

		_, err := client.request(http.MethodPost, "/level?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "ok")
		return err

	case "delete":
		fs := flag.NewFlagSet("level delete", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		pathFlag := fs.String("path", "", "Path prefix to remove")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*pathFlag) == "" {
			return errors.New("level delete requires --path")
		}

		q := url.Values{}
		q.Set("path", *pathFlag)
		_, err := client.request(http.MethodDelete, "/level?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "ok")
		return err

	default:
		return fmt.Errorf("unknown level action %q", args[0])
	}
}

func runEnable(client *apiClient, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("enable requires one of: on, off, set")
	}

	var enabled string
	switch args[0] {
	case "on":
		enabled = "true"
	case "off":
		enabled = "false"
	case "set":
		fs := flag.NewFlagSet("enable set", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		value := fs.Bool("enable", true, "Enable logging if true, disable if false")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		enabled = fmt.Sprintf("%t", *value)
	default:
		return fmt.Errorf("unknown enable action %q", args[0])
	}

	q := url.Values{}
	q.Set("enable", enabled)
	_, err := client.request(http.MethodPost, "/enable?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "ok")
	return err
}

func runTraceID(client *apiClient, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("trace-id requires one of: get, set, clear")
	}

	switch args[0] {
	case "get":
		body, err := client.request(http.MethodGet, "/trace-id", nil)
		if err != nil {
			return err
		}
		_, err = out.Write(body)
		return err

	case "set":
		fs := flag.NewFlagSet("trace-id set", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		traceID := fs.String("value", "", "Trace ID to filter")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*traceID) == "" {
			return errors.New("trace-id set requires --value")
		}

		q := url.Values{}
		q.Set("trace-id", *traceID)
		_, err := client.request(http.MethodPost, "/trace-id?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "ok")
		return err

	case "clear":
		_, err := client.request(http.MethodDelete, "/trace-id", nil)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "ok")
		return err

	default:
		return fmt.Errorf("unknown trace-id action %q", args[0])
	}
}

type apiClient struct {
	baseURL string
	http    *http.Client
}

func (c *apiClient) request(method, endpoint string, body io.Reader) ([]byte, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid --host %q: %w", c.baseURL, err)
	}

	ep, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}

	u.Path = path.Join(u.Path, ep.Path)
	u.RawQuery = ep.RawQuery

	req, err := http.NewRequest(method, u.String(), body)
	if err != nil {
		return nil, err
	}

	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("request failed: %s: %s", res.Status, bytes.TrimSpace(data))
	}

	if len(data) == 0 {
		return []byte("\n"), nil
	}

	if json.Valid(data) && !bytes.HasSuffix(data, []byte("\n")) {
		data = append(data, '\n')
	}

	return data, nil
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "slogx - CLI for slogx runtime log control")
	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "Usage:\n  slogx [--host %s] <command>\n\n", defaultHost)
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  level get")
	fmt.Fprintln(out, "  level set --path myapp/db --level DEBUG")
	fmt.Fprintln(out, "  level set --level INFO                  # sets default")
	fmt.Fprintln(out, "  level delete --path myapp/db")
	fmt.Fprintln(out, "  enable on|off")
	fmt.Fprintln(out, "  enable set --enable=true|false")
	fmt.Fprintln(out, "  trace-id get")
	fmt.Fprintln(out, "  trace-id set --value abc-123")
	fmt.Fprintln(out, "  trace-id clear")
}
