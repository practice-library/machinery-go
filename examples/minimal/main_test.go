package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestMain runs main itself when the test binary is started as the example (MINIMAL_MAIN=1).
func TestMain(m *testing.M) {
	if os.Getenv("MINIMAL_MAIN") == "1" {
		os.Args = append([]string{os.Args[0]}, strings.Fields(os.Getenv("MINIMAL_ARGS"))...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const svc = "/machinery.plugin.v1alpha1.PluginService/"

// call runs the example once over stdio: its exit status, its status line and its body.
func call(t *testing.T, path, timeoutMs, body string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "MINIMAL_MAIN=1", "MINIMAL_ARGS=-stdio")
	cmd.Stdin = strings.NewReader(`{"path": "` + path + `", "timeoutMs": ` + timeoutMs + "}\n" + body)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	exit := 0
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	line, rest, _ := strings.Cut(out.String(), "\n")
	return exit, line, rest
}

func TestTheExampleOverStdioExitsZeroOnOkAndOneOnAnyError(t *testing.T) {
	caps := `"caps": {"timeSeconds": 60, "outputBytes": 1048576}`
	exit, line, body := call(t, svc+"Describe", "60000", "{"+caps+"}")
	if exit != 0 || line != `{"status":"ok"}` || !strings.Contains(body, `"name":"minimal"`) {
		t.Errorf("%d %s %s", exit, line, body)
	}
	exit, line, _ = call(t, svc+"RunUnit", "60000", "{"+caps+`, "input": {}, "network": {}}`)
	if exit != 1 || line != `{"status":"unavailable"}` {
		t.Errorf("%d %s", exit, line)
	}
	exit, line, _ = call(t, svc+"Run", "60000", "{"+caps+`, "input": {}, "network": {}}`)
	if exit != 1 || line != `{"status":"unimplemented"}` {
		t.Errorf("%d %s", exit, line)
	}
}

func TestTheExampleAnswersOnTimeAndExitsWhileItsPlanStillSleeps(t *testing.T) {
	started := time.Now()
	exit, line, _ := call(t, svc+"Plan", "60000", `{"caps": {"timeSeconds": 1, "outputBytes": 1048576}, "input": {"nodes": []}}`)
	if exit != 1 || line != `{"status":"deadline_exceeded"}` || time.Since(started) > 3*time.Second {
		t.Errorf("%d %s %v", exit, line, time.Since(started))
	}
}
