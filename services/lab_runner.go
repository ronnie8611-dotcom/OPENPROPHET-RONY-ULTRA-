package services

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"prophet-trader/config"
)

var (
	labRunMu    sync.Mutex
	labRunning  bool
	labLastRun  LabRunStatus
	labLogName  = "lab_run.log"
	labScripts  = []string{"ml_trainer.py", "validate_raschke.py", "backtest_lab.py", "backtest_correcto.py", "camino_b_edges.py"}
	labAllowed  = map[string]bool{"ml_trainer.py": true, "validate_raschke.py": true, "backtest_lab.py": true, "backtest_correcto.py": true, "camino_b_edges.py": true}
)

type LabRunStatus struct {
	Running    bool       `json:"running"`
	Script     string     `json:"script"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExitCode   int        `json:"exit_code"`
	LogFile    string     `json:"log_file"`
	Tail       string     `json:"tail"`
}

func LabScripts() []string {
	out := append([]string(nil), labScripts...)
	sort.Strings(out)
	return out
}

func labDir() string {
	if config.AppConfig != nil && config.AppConfig.LabDir != "" {
		return config.AppConfig.LabDir
	}
	return "Backtest_Lab"
}

func pythonPath() string {
	if config.AppConfig != nil && config.AppConfig.PythonPath != "" {
		return config.AppConfig.PythonPath
	}
	return "python"
}

func tailText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}

// RunLabScript launches one whitelisted Python script asynchronously.
func RunLabScript(script string) error {
	if !labAllowed[script] {
		return fmt.Errorf("script %q is not allowed", script)
	}

	labRunMu.Lock()
	if labRunning {
		labRunMu.Unlock()
		return fmt.Errorf("a lab script is already running")
	}
	labRunning = true
	now := time.Now()
	logPath := filepath.Join(labDir(), labLogName)
	labLastRun = LabRunStatus{Running: true, Script: script, StartedAt: &now, ExitCode: -1, LogFile: logPath}
	labRunMu.Unlock()

	go func() {
		f, _ := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if f != nil {
			fmt.Fprintf(f, "\n=== %s %s ===\n", time.Now().Format(time.RFC3339), script)
		}

		cmd := exec.Command(pythonPath(), script)
		cmd.Dir = labDir()

		var buf bytes.Buffer
		if f != nil {
			cmd.Stdout = io.MultiWriter(f, &buf)
			cmd.Stderr = cmd.Stdout
		} else {
			cmd.Stdout = &buf
			cmd.Stderr = &buf
		}

		err := cmd.Run()
		exit := 0
		if err != nil {
			exit = 1
			if ee, ok := err.(*exec.ExitError); ok {
				exit = ee.ExitCode()
			}
		}
		finish := time.Now()

		labRunMu.Lock()
		labRunning = false
		labLastRun.Running = false
		labLastRun.ExitCode = exit
		labLastRun.FinishedAt = &finish
		labLastRun.Tail = tailText(buf.String(), 6000)
		labRunMu.Unlock()

		if f != nil {
			fmt.Fprintf(f, "\n=== fin exit=%d ===\n", exit)
			f.Close()
		}
	}()

	return nil
}

// LabStatusSnapshot returns the current/last run and its recent output.
func LabStatusSnapshot() LabRunStatus {
	labRunMu.Lock()
	status := labLastRun
	labRunMu.Unlock()

	if status.Tail == "" {
		if raw, err := os.ReadFile(filepath.Join(labDir(), labLogName)); err == nil {
			status.Tail = tailText(string(raw), 6000)
		}
	}
	return status
}
