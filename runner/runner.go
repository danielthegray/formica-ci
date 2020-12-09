package runner

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const configFolder = "formica_conf"
const configInitPrefix = "config_init"
const updatePrefix = "update"
const agentInitPrefix = "agent_init"
const runPrefix = "run"
const generatePrefix = "gen"
const jobQueue = "job_queue"
const formicaRuns = "formica_runs"

// UpdateEnabled set to true at launch will enable live updating of the configuration
const UpdateEnabled = false

// ShutdownNotifiers is a collection of channels used to notify of different shutdown events
type ShutdownNotifiers struct {
	Slow             <-chan struct{}
	Immediate        <-chan struct{}
	ForceTermination <-chan struct{}
}

var existingJobs []string

func fetchConfigFolder() error {
	currentDir, err := os.Getwd()
	if err != nil {
		log.Println("Error when getting the current dir")
		return err
	}
	scriptName, err := FindScript(currentDir, configInitPrefix)
	if err != nil {
		log.Println("Error when finding the script")
		return err
	}
	_, err = ExecuteScript(currentDir, scriptName)
	if err != nil {
		log.Println("Error when executing the script")
		return err
	}
	return nil
}

func ensureConfigFolderPresent() error {
	confStat, err := os.Stat(configFolder)
	if err != nil {
		if os.IsNotExist(err) {
			configErr := fetchConfigFolder()
			if configErr != nil {
				return fmt.Errorf("error when fetching configuration folder: %s", configErr.Error())
			}
		} else {
			return fmt.Errorf("unexpected error when checking for permissions of configuration folder '%s': %s", configFolder, err.Error())
		}
	}
	if confStat != nil && !confStat.IsDir() {
		return fmt.Errorf("configuration folder path '%s' was already occupied, and was not a folder", configFolder)
	}

	return nil
}

func updateConfig() error {
	updateScriptFile, err := FindScript(configFolder, updatePrefix)
	if err != nil {
		return fmt.Errorf("error while finding update script in configuration: %s", err.Error())
	}
	if UpdateEnabled {
		_, err = ExecuteScript(configFolder, updateScriptFile)
		if err != nil {
			return fmt.Errorf("error while updating configuration: %s", err.Error())
		}
	}
	// after updating the configuration, we reload the jobs that exist
	return reloadJobs()
}

func reloadJobs() error {
	existingJobs = nil
	// TODO: deal with jobs that have disappeared but are still running
	// TODO: deal with new jobs
	// TODO: deal with existing jobs
	return filepath.Walk(configFolder, func(file string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasPrefix(info.Name(), agentInitPrefix) {
			jobPathSegments := strings.SplitN(path.Dir(file), string(os.PathSeparator), 2)
			jobPath := jobPathSegments[1]
			log.Printf("Found job %s", jobPath)
			existingJobs = append(existingJobs, jobPath)
		}
		return nil
	})
}

func launchBackgroundUpdater() chan<- struct{} {
	updaterStopEvent := make(chan struct{}, 1)
	// TODO: parse from config
	jobUpdateDelay := 5 * time.Minute
	go func() {
		for {
			select {
			case <-updaterStopEvent:
				return
			case <-time.After(jobUpdateDelay):
				err := updateConfig()
				if err != nil {
					log.Printf("Unexpected error while keeping configuration updated: %s", err)
				}
			}
		}
	}()
	return updaterStopEvent
}

func isJobNotFound(jobName string) bool {
	for _, validJobName := range existingJobs {
		if validJobName == jobName {
			return false
		}
	}
	return true
}

func generateJobRunCode(jobName string) (string, error) {
	jobFolder := filepath.Join(configFolder, jobName)
	// we only run this script if it is found, if not, then no problem
	localScriptOutput := ""
	runLocalScript, err := FindScript(jobFolder, generatePrefix)
	if err == nil {
		localScriptOutput, err = ExecuteScript(jobFolder, runLocalScript)
		if err != nil {
			return "", fmt.Errorf("error while running %s script: %s", generatePrefix, err.Error())
		}
	}
	// the run script is transferred and run on the remote machine
	runScript, err := FindScript(jobFolder, runPrefix)
	if err != nil {
		return "", fmt.Errorf("couldn't find %s script for job %s: %s", runPrefix, jobName, err.Error())
	}
	jobScript, err := TransferAndRunScriptCommand(filepath.Join(jobFolder, runScript), "/tmp/formica_agent")
	if err != nil {
		return "", fmt.Errorf("error while preparing job run script: %s", err.Error())
	}
	// we combine the locally generated shell code + the job transfer/run commands
	jobScript = localScriptOutput + jobScript

	log.Printf("The job scripts is %s", jobScript)

	return jobScript, nil
}

func prepareRunFolder(jobName string) (string, error) {
	runFolder := filepath.Join(formicaRuns, jobName)
	_ = os.MkdirAll(runFolder, 0750)

	files, err := ioutil.ReadDir(runFolder)
	if err != nil {
		return "", fmt.Errorf("error while checking inside run folder %s: %s", runFolder, err.Error())
	}
	attempts := 0
	// we just have an error so that we keep trying to get a "free" sequence number
	// for the job runs
	err = fmt.Errorf("dummy error")
	for attempts < 10 && err != nil {
		maxIndex := 1
		for _, file := range files {
			if !file.IsDir() {
				continue
			}
			if runIndex, err := strconv.Atoi(file.Name()); err == nil {
				if runIndex >= maxIndex {
					maxIndex++
				}
			}
		}
		runFolder = filepath.Join(runFolder, fmt.Sprintf("%d", maxIndex))

		err = os.Mkdir(runFolder, 0750)
		if err != nil {
			log.Printf("Error while preparing run folder %s: %s", runFolder, err.Error())
		}
		attempts++
	}
	return runFolder, nil
}

func runJob(jobName string) (*exec.Cmd, error) {
	jobFolder := filepath.Join(configFolder, jobName)
	log.Printf("Launching job from %s", jobFolder)
	agentInitScript, err := FindScript(jobFolder, agentInitPrefix)
	if err != nil {
		return nil, fmt.Errorf("error while searching for %s script: %s", agentInitPrefix, err.Error())
	}

	jobScript, err := generateJobRunCode(jobName)
	if err != nil {
		return nil, fmt.Errorf("error while preparing job run code: %s", err.Error())
	}

	jobRunFolder, err := prepareRunFolder(jobName)
	if err != nil {
		return nil, fmt.Errorf("error while setting up job run folder: %s", err.Error())
	}

	stdoutWriter, err := os.Create(filepath.Join(jobRunFolder, "stdout.log"))
	if err != nil {
		return nil, fmt.Errorf("error when opening up stdout.log file: %s", err.Error())
	}
	stderrWriter, err := os.Create(filepath.Join(jobRunFolder, "stderr.log"))
	if err != nil {
		return nil, fmt.Errorf("error when opening up stderr.log file: %s", err.Error())
	}

	stdin := strings.NewReader(jobScript)
	newJob := PrepareCommand(jobFolder, agentInitScript, stdin, stdoutWriter, stderrWriter)
	newJob.Start()
	go func() {
		newJob.Wait()
	}()
	return newJob, nil
}

func launchJobRunner() (receiver chan<- string, stopNotifier chan<- struct{}) {
	runnerStopEvent := make(chan struct{}, 1)
	jobReceiver := make(chan string)
	var runningJobs []exec.Cmd
	go func() {
		for {
			select {
			case <-runnerStopEvent:
				for _, job := range runningJobs {
					job.Wait()
				}
				return
			case jobToRun := <-jobReceiver:
				if isJobNotFound(jobToRun) {
					log.Printf("job %s is not found in the configuration folder!", jobToRun)
					continue
				}
				newJob, err := runJob(jobToRun)
				if err != nil {
					log.Printf("error while running job '%s': %s", jobToRun, err.Error())
					continue
				}
				runningJobs = append(runningJobs, *newJob)
			}
		}

	}()
	return jobReceiver, runnerStopEvent
}

func launchJobQueueListener(jobReceiver chan<- string) chan<- struct{} {
	jobQueueStopEvent := make(chan struct{}, 1)
	_ = os.Mkdir(jobQueue, 0777)
	queuePollDelay := 1 * time.Second
	go func() {
		for {
			select {
			case <-jobQueueStopEvent:
				err := os.RemoveAll(jobQueue)
				if err != nil {
					log.Printf("error when cleaning up job queue folder: %s", err.Error())
				}
				return
			case <-time.After(queuePollDelay):
				filesInQueue, err := ioutil.ReadDir(jobQueue)
				if err != nil {
					log.Printf("error when listing jobs in queue: %s", err.Error())
					continue
				}
				for _, enqueuedJob := range filesInQueue {
					fullJobFilename := path.Join(jobQueue, enqueuedJob.Name())
					specifiedJob, err := ioutil.ReadFile(fullJobFilename)
					if err != nil {
						log.Printf("error while opening job file %s: %s", fullJobFilename, err.Error())
					} else {
						jobName := strings.TrimSpace(string(specifiedJob))
						jobReceiver <- jobName
						log.Printf("Found job %s in %s", jobName, fullJobFilename)
					}
					err = os.Remove(fullJobFilename)
					if err != nil {
						log.Printf("failed to cleanup job file %s: %s", fullJobFilename, err.Error())
					}
				}
			}
		}
	}()
	return jobQueueStopEvent
}

// Start initializes the job runner
func Start(shutdownNotifiers *ShutdownNotifiers) {
	err := ensureConfigFolderPresent()
	if err != nil {
		log.Fatalf("%s", err)
	}
	err = updateConfig()
	if err != nil {
		log.Fatalf("configuration is not updateable: %s", err)
	}
	updaterStop := launchBackgroundUpdater()
	jobReceiver, jobRunnerStop := launchJobRunner()
	jobQueueListenerStop := launchJobQueueListener(jobReceiver)

	log.Println("Formica CI is now running")

	for {
		select {
		case <-shutdownNotifiers.Slow:
			// notify updater to stop
			updaterStop <- struct{}{}
			jobRunnerStop <- struct{}{}
			jobQueueListenerStop <- struct{}{}
			break
		case <-shutdownNotifiers.Immediate:
			break
		case <-shutdownNotifiers.ForceTermination:
			break
		}
	}
}
