package mcptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// RunFromEnv serves a scenario from the environment and exits, when the
// variable is set. A test package calls it from TestMain so its own test
// binary can be the stdio server a client launches.
func RunFromEnv() {
	raw := os.Getenv(EnvScenario)
	if raw == "" {
		return
	}
	var scn Scenario
	if err := json.Unmarshal([]byte(raw), &scn); err != nil {
		fmt.Fprintf(os.Stderr, "mcptest: bad scenario: %v\n", err)
		os.Exit(2)
	}
	if err := Serve(scn, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "mcptest: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// writePID writes a process ID to path through a rename, so a test polling
// the file never reads it half written.
func writePID(path string, pid int) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ScenarioEnv renders a scenario for a subprocess's environment.
func ScenarioEnv(scn Scenario) (string, error) {
	data, err := json.Marshal(scn)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Serve speaks the stdio binding until stdin ends. Requests are handled
// concurrently, because a legacy server request has to wait for the
// client's reply, which arrives on the same stdin the loop reads.
func Serve(scn Scenario, stdin io.Reader, stdout, stderr io.Writer) error {
	if scn.IgnoreSIGTERM {
		signal.Ignore(syscall.SIGTERM)
	}
	if scn.PIDPath != "" {
		if err := writePID(scn.PIDPath, os.Getpid()); err != nil {
			return err
		}
	}
	if scn.SpawnDescendant != "" {
		descendant := exec.Command("sleep", "60")
		if err := descendant.Start(); err != nil {
			return fmt.Errorf("start descendant: %w", err)
		}
		if err := writePID(scn.SpawnDescendant, descendant.Process.Pid); err != nil {
			return err
		}
	}
	for _, line := range scn.StderrLines {
		fmt.Fprintln(stderr, line)
	}
	s := newServer(scn)
	var writeMu sync.Mutex
	emit := func(msg *Message) {
		data, err := json.Marshal(msg)
		if err != nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_, _ = stdout.Write(append(data, '\n'))
	}

	reader := bufio.NewReaderSize(stdin, 1<<20)
	var handlers sync.WaitGroup
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var msg Message
			if json.Unmarshal(line, &msg) == nil {
				s.recordMessage(&msg)
				switch {
				case msg.IsResponse():
					s.deliverResponse(&msg)
				case len(msg.ID) == 0:
					s.handleNotification(&msg)
				case scn.StallAfter != "" && msg.Method == scn.StallAfter:
					for _, r := range s.handleRequest(&msg, "stdio") {
						emit(r.msg)
					}
					// Never read stdin again.
					stayAlive()
				default:
					handlers.Add(1)
					go func(msg *Message) {
						defer handlers.Done()
						for _, r := range s.handleRequest(msg, "stdio") {
							emit(r.msg)
							if r.wait != nil {
								<-r.wait
								s.forgetRequest(r.msg.ID)
							}
						}
					}(&msg)
				}
			}
		}
		if err != nil {
			break
		}
	}
	handlers.Wait()
	if scn.IgnoreStdinClose {
		stayAlive()
	}
	return nil
}

// stayAlive never returns. It sleeps rather than blocking in an empty
// select, which the runtime ends as a deadlock on Linux, so the subprocess
// stays alive until a signal ends it.
func stayAlive() {
	for {
		time.Sleep(time.Hour)
	}
}
