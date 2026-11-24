package services

import "testing"

func TestLabScriptsAndWhitelist(t *testing.T) {
	scripts := LabScripts()
	if len(scripts) == 0 {
		t.Fatal("LabScripts returned nothing")
	}
	if err := RunLabScript("evil.py"); err == nil {
		t.Fatal("RunLabScript must reject scripts outside the whitelist")
	}
}
