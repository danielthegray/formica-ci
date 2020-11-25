package runner

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// FindScriptError is an error type for the result of finding a script to run
type FindScriptError struct {
	text         string
	foundScripts []string
}

func (err *FindScriptError) Error() string {
	return err.text
}

// IsNoScriptFoundError returns true if the error is due to no script being found
func (err *FindScriptError) IsNoScriptFoundError() bool {
	return len(err.foundScripts) == 0
}

// IsManyScriptsFoundError returns true if the error is due to finding more than one script with the specified name (only one should exist)
func (err *FindScriptError) IsManyScriptsFoundError() bool {
	return len(err.foundScripts) > 1
}

func newScriptNotFoundError(scriptFolder, scriptName string) *FindScriptError {
	return &FindScriptError{
		text:         fmt.Sprintf("no %s script found in %s", scriptName, scriptFolder),
		foundScripts: []string{},
	}
}
func newTooManyScriptsError(scriptFolder, scriptName string, foundScripts []string) *FindScriptError {
	return &FindScriptError{
		text:         fmt.Sprintf("too many scripts for %s found in %s", scriptName, scriptFolder),
		foundScripts: foundScripts,
	}
}

// FindScript looks for a script with a certain prefix/name in the specified folder
// since most jobs in Formica CI are done with scripts, the name of the script will
// define what job it is called for
func FindScript(scriptFolder string, scriptName string) (string, *FindScriptError) {
	var matchingScripts []string
	filesInFolder, err := ioutil.ReadDir(scriptFolder)
	if err != nil {
		log.Fatalf("Failed listing directory: %s", err)
	}
	// we look for files in the folder which start with the script name
	for _, file := range filesInFolder {
		if file.IsDir() {
			continue
		}
		if strings.HasPrefix(file.Name(), scriptName) {
			matchingScripts = append(matchingScripts, file.Name())
		}
	}
	if len(matchingScripts) == 0 {
		return "", newScriptNotFoundError(scriptFolder, scriptName)
	}
	if len(matchingScripts) > 1 {
		return "", newTooManyScriptsError(scriptFolder, scriptName, matchingScripts)
	}
	return matchingScripts[0], nil
}

func PrepareCommand(parentFolder, scriptFile string, stdin *io.Reader, stdout *io.Writer, stderr *io.Writer) *exec.Cmd {
	scriptAbsolutePath := filepath.Join(parentFolder, scriptFile)
	var shellPath string
	var err error
	var firstArg string
	if runtime.GOOS == "windows" {
		shellPath, err = exec.LookPath("cmd")
		if err != nil {
			log.Fatal("cmd not found in PATH!")
		}
		firstArg = "/C"
	} else {
		shellPath, err = exec.LookPath("sh")
		if err != nil {
			log.Fatal("sh not found in PATH!")
		}
		firstArg = "-c"
	}
	return &exec.Cmd{
		Path:   shellPath,
		Args:   []string{firstArg, scriptAbsolutePath},
		Dir:    parentFolder,
		Stdin:  *stdin,
		Stdout: *stdout,
		Stderr: *stderr,
	}
}
