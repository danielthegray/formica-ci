package runner

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Prefix is a type specific for script prefixes
type Prefix string

// FormicaScript is a type specific for scripts that corrrespond to a prefix (to ensure that the correct process is followed)
type FormicaScript string

// TimestampedLogger is a WriteCloser implementation that outputs to a normal log file, as well as
// to another file with one UNIX epoch timestamp per line written (to match up later)
type TimestampedLogger struct {
	logFile       io.WriteCloser
	timestampFile io.WriteCloser
}

func (tl *TimestampedLogger) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	toWrite := string(p)
	numberOfContainedLines := strings.Count(toWrite, "\n")
	for i := 0; i < numberOfContainedLines; i++ {
		currentUnixTime := fmt.Sprintf("%d\n", time.Now().UnixNano())
		_, err := tl.timestampFile.Write([]byte(currentUnixTime))
		if err != nil {
			return 0, err
		}
	}
	return tl.logFile.Write(p)
}

// Close closes both of the underlying files (log + timestamp)
func (tl *TimestampedLogger) Close() error {
	errL := tl.logFile.Close()
	errT := tl.timestampFile.Close()
	if errL != nil || errT != nil {
		errorMessage := ""
		if errL != nil {
			errorMessage = errorMessage + errL.Error()
		}
		if errT != nil {
			errorMessage = errorMessage + errT.Error()
		}
		return fmt.Errorf("Error(s) when closing timestamped log: %s", errorMessage)
	}
	return nil
}

// BuildTimestampedLogger returns a writer that writes a ".log" file with the normal output
// as well as a ".timestamp" file with the timestamps for each line
func BuildTimestampedLogger(logFolder, baseName string) (*TimestampedLogger, error) {
	log.WithFields(log.Fields{
		"log_folder": logFolder,
		"basename":   baseName,
	}).Debug("Building timestamped logger")
	logFilePath := filepath.Join(logFolder, baseName+".log")
	timestampFilePath := filepath.Join(logFolder, baseName+".timestamp")
	logFile, err := os.Create(logFilePath)
	if err != nil {
		return nil, fmt.Errorf("error when creating log file: %s", err.Error())
	}
	timestampFile, err := os.Create(timestampFilePath)
	if err != nil {
		return nil, fmt.Errorf("error when creating timestamp file: %s", err.Error())
	}
	return &TimestampedLogger{
		logFile,
		timestampFile,
	}, nil
}

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

func newScriptNotFoundError(scriptFolder string, scriptPrefix Prefix) *FindScriptError {
	return &FindScriptError{
		text:         fmt.Sprintf("no %s script found in %s", scriptPrefix, scriptFolder),
		foundScripts: []string{},
	}
}
func newTooManyScriptsError(scriptFolder string, scriptPrefix Prefix, foundScripts []string) *FindScriptError {
	return &FindScriptError{
		text:         fmt.Sprintf("too many scripts for %s found in %s", scriptPrefix, scriptFolder),
		foundScripts: foundScripts,
	}
}

// FindScript looks for a script with a certain prefix/name in the specified folder
// since most jobs in Formica CI are done with scripts, the name of the script will
// define what job it is called for. The existence of two scripts with the same prefix
// is considered an error, except when the second option is *.bat, in which case, the
// *.bat script will be returned only if we are running on Windows.
func FindScript(scriptFolder string, scriptPrefix Prefix) (FormicaScript, *FindScriptError) {
	var matchingScripts []string
	filesInFolder, err := ioutil.ReadDir(scriptFolder)
	if err != nil {
		log.Fatalf("Failed listing directory '%s' for script search", err)
	}
	// we look for files in the folder which start with the script name
	for _, file := range filesInFolder {
		if file.IsDir() {
			continue
		}
		if file.Name() == string(scriptPrefix) || strings.HasPrefix(file.Name(), string(scriptPrefix)+".") {
			matchingScripts = append(matchingScripts, file.Name())
		}
	}
	if len(matchingScripts) == 0 {
		return "", newScriptNotFoundError(scriptFolder, scriptPrefix)
	}
	if len(matchingScripts) == 1 {
		return FormicaScript(matchingScripts[0]), nil
	}
	if len(matchingScripts) == 2 {
		batIndex := -1
		for index, scriptResult := range matchingScripts {
			if scriptResult == string(scriptPrefix)+".bat" {
				batIndex = index
				break
			}
		}
		if batIndex == -1 {
			// the other script found is not a .bat script
			return "", newTooManyScriptsError(scriptFolder, scriptPrefix, matchingScripts)
		}
		if runtime.GOOS == "windows" {
			return FormicaScript(matchingScripts[batIndex]), nil
		}
		notBatIndex := 1 - batIndex // the index of the script that is not the .bat file
		return FormicaScript(matchingScripts[notBatIndex]), nil
	}
	return "", newTooManyScriptsError(scriptFolder, scriptPrefix, matchingScripts)
}

// FindScriptInJobOrParents looks for a script not only in a job folder but in the entire hierarchy of jobs/job-groups
// and returns the folder and script name where the sought for script is, and a potential error. If no script is found,
// two blank strings and a nil error will be returned.
func FindScriptInJobOrParents(rootFolder string, leafPath string, scriptPrefix Prefix) (string, FormicaScript, error) {
	scriptLocation, err := filepath.Abs(filepath.Join(rootFolder, leafPath))
	if err != nil {
		return "", "", fmt.Errorf("error while building absolute path to script search leaf folder: %s", err.Error())
	}
	absoluteRootFolder, err := filepath.Abs(rootFolder)
	if err != nil {
		return "", "", fmt.Errorf("error while building absolute path to search root folder: %s", err.Error())
	}
	for scriptLocation != absoluteRootFolder {
		script, findErr := FindScript(scriptLocation, scriptPrefix)
		if findErr == nil {
			return scriptLocation, script, nil
		}
		if !findErr.IsNoScriptFoundError() {
			return "", "", findErr
		}
		scriptLocation = filepath.Dir(scriptLocation)
	}
	// we did not find the script in the job or any parent, so there is no related script in the job's hierarchy
	return "", "", nil
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

// TransferAndRunScriptCommand generates shell commands that create the specified localFile on the agent's side and then executes it
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

// PrepareCommandWithoutInput prepares a command to be executed without any input, and returning strings.Builder
// objects which can be used to read back the stdout/stderr output of the process
func PrepareCommandWithoutInput(parentFolder string, scriptFile FormicaScript) (*exec.Cmd, *strings.Builder, *strings.Builder) {
	stdin := strings.NewReader("")
	stdout := &strings.Builder{}
	stderr := &strings.Builder{}
	return PrepareCommand(parentFolder, scriptFile, stdin, stdout, stderr), stdout, stderr
}

// PrepareCommand sets up a command ready to be executed and wires in stdin/stdout/stderr readers/writers
func PrepareCommand(parentFolder string, scriptFile FormicaScript, stdin io.Reader, stdout io.Writer, stderr io.Writer) *exec.Cmd {
	log.WithFields(log.Fields{
		"script_file":   scriptFile,
		"parent_folder": parentFolder,
	}).Debug("Preparing script execution")
	if !strings.HasPrefix(parentFolder, "/") {
		currentDir, err := os.Getwd()
		if err != nil {
			log.Fatal("Error when getting the current dir")
		}
		parentFolder = filepath.Join(currentDir, parentFolder)
	}
	scriptAbsolutePath := filepath.Join(parentFolder, string(scriptFile))
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

// FindAndExecute will find a script with the desired prefix in the specified folder and execute it
func FindAndExecute(parentPath string, scriptPrefix Prefix) (string, error) {
	script, findErr := FindScript(parentPath, scriptPrefix)
	if findErr != nil {
		return "", fmt.Errorf("error while searching for %s script: %s", scriptPrefix, findErr.Error())
	}
	return OutputOfExecuting(parentPath, script)
}

// OutputOfExecuting runs a script located inside a specified path, and returns the stdout output in a string, or a potential error
func OutputOfExecuting(parentPath string, script FormicaScript) (string, error) {
	output := new(bytes.Buffer)
	emptyReader := strings.NewReader("")
	scriptCommand := PrepareCommand(parentPath, script, emptyReader, output, ioutil.Discard)
	err := scriptCommand.Run()
	if err != nil {
		return "", err
	}
	return output.String(), nil
}
