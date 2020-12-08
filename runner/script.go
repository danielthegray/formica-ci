package runner

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
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

func newScriptNotFoundError(scriptFolder, scriptName string) FindScriptError {
	return FindScriptError{
		text:         fmt.Sprintf("no %s script found in %s", scriptName, scriptFolder),
		foundScripts: []string{},
	}
}
func newTooManyScriptsError(scriptFolder, scriptName string, foundScripts []string) FindScriptError {
	return FindScriptError{
		text:         fmt.Sprintf("too many scripts for %s found in %s", scriptName, scriptFolder),
		foundScripts: foundScripts,
	}
}

// FindScript looks for a script with a certain prefix/name in the specified folder
// since most jobs in Formica CI are done with scripts, the name of the script will
// define what job it is called for
func FindScript(scriptFolder string, scriptPrefix string) (string, error) {
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
		if strings.HasPrefix(file.Name(), scriptPrefix) {
			matchingScripts = append(matchingScripts, file.Name())
		}
	}
	if len(matchingScripts) == 0 {
		return "", fmt.Errorf("no %s script found in %s", scriptPrefix, scriptFolder)
	}
	if len(matchingScripts) > 1 {
		return "", fmt.Errorf("too many %s scripts found in %s", scriptPrefix, scriptFolder)
	}
	return matchingScripts[0], nil
}

// FileTransferCommand builds a series of shell commands to place a local file on the server, at a specific destination path
// using only a direct terminal connection (it assumes that the client has the "base64" utility installed)
func FileTransferCommand(localFileToTransfer, pathOnDestination string) (string, error) {
	filename := filepath.Base(localFileToTransfer)
	var destinationFolder string
	if strings.HasSuffix(pathOnDestination, string(filepath.Separator)+filename) {
		destinationFolder = filepath.Dir(pathOnDestination)
	} else {
		destinationFolder = pathOnDestination
	}
	remoteFilePath := filepath.Join(destinationFolder, filename)
	// we will try to be conservative with the command line limit
	const CommandLengthLimit = 32000
	transferContents, err := ioutil.ReadFile(localFileToTransfer)
	if err != nil {
		return "", err
	}
	localFileInBase64 := base64.StdEncoding.EncodeToString(transferContents)

	commandStart := "echo '%s' | base64 -d > " + remoteFilePath
	commandAppend := "echo '%s' | base64 -d >> " + remoteFilePath
	useCommandRest := false
	result := strings.Builder{}
	// we ensure that the path exists on the destination side
	result.WriteString("mkdir -p " + destinationFolder + "\n")
	blockSizeLimit := CommandLengthLimit - len(commandAppend)
	for len(localFileInBase64) > blockSizeLimit {
		command := commandAppend
		if !useCommandRest {
			command = commandStart
			useCommandRest = true
		}
		blockToSend := localFileInBase64[0:blockSizeLimit]
		localFileInBase64 = localFileInBase64[blockSizeLimit:]
		result.WriteString(fmt.Sprintf(command+"\n", blockToSend))
	}
	command := commandAppend
	if !useCommandRest {
		command = commandStart
		// not-needed
		// useCommandRest = true
	}
	result.WriteString(fmt.Sprintf(command+"\n", localFileInBase64))

	return result.String(), nil
}

func TransferAndRunScriptCommand(localFile, remoteExecDir string) (string, error) {
	localFileName := filepath.Base(localFile)
	fileTransferCommands, err := FileTransferCommand(localFile, remoteExecDir)
	if err != nil {
		return "", fmt.Errorf("error while building file transfer command: %s", err.Error())
	}
	fileTransferCommands = "set -e\n" + fileTransferCommands +
		"cd " + remoteExecDir + "\n" +
		"chmod +x " + localFileName + "\n" +
		"sh ./" + localFileName + "\n" +
		"echo STEP_RET_CODE=$?\n"
	return fileTransferCommands, nil
}

// PrepareCommand sets up a command ready to be executed and wires in stdin/stdout/stderr readers/writers
func PrepareCommand(parentFolder, scriptFile string, stdin io.Reader, stdout io.Writer, stderr io.Writer) *exec.Cmd {
	log.Printf("Running script %s in folder %s", scriptFile, parentFolder)
	if !strings.HasPrefix(parentFolder, "/") {
		currentDir, err := os.Getwd()
		if err != nil {
			log.Fatal("Error when getting the current dir")
		}
		parentFolder = filepath.Join(currentDir, parentFolder)
	}
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
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	}
}

// ExecuteScript runs a script located inside a specified path
func ExecuteScript(parentPath, scriptFilename string) (string, error) {
	output := new(bytes.Buffer)
	emptyReader := strings.NewReader("")
	scriptCommand := PrepareCommand(parentPath, scriptFilename, emptyReader, output, ioutil.Discard)
	err := scriptCommand.Run()
	if err != nil {
		return "", err
	}
	return output.String(), nil
}
