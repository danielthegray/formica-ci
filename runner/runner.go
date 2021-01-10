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
	"sync"
	"time"
)

const configFolder string = "formica_conf"
const jobQueue = "job_queue"
const formicaRuns = "formica_runs"
const versionTag = "version.tag"

const configInit Prefix = "config_init"
const configRemoteVersion Prefix = "config_version_remote"
const configLocalVersion Prefix = "config_version_local"
const configUpdate Prefix = "config_update"
const agentInit Prefix = "agent_init"
const agentCleanup Prefix = "agent_cleanup"
const versionCheck Prefix = "version_check"
const versionTimestamp Prefix = "version_timestamp"
const branchList Prefix = "branch_list"
const run Prefix = "run"
const generate Prefix = "gen"

const stdoutPrefix = "stdout"
const stderrPrefix = "stderr"

// ShutdownNotifiers is a collection of channels used to notify of different shutdown events
type ShutdownNotifiers struct {
	Slow             <-chan struct{}
	Immediate        <-chan struct{}
	ForceTermination <-chan struct{}
}

type jobList struct {
	sync.Mutex
	jobs []string
}

var existingJobs jobList
var versionedJobs jobList

func fetchConfigFolder() error {
	currentDir, err := os.Getwd()
	if err != nil {
		log.Println("Error when getting the current dir")
		return err
	}
	_, err = FindAndExecute(currentDir, configInit)
	if err != nil {
		log.Println("Error when fetching the configuration folder! %s", err.Error())
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
	updateScriptFile, findErr := FindScript(configFolder, configUpdate)
	if findErr != nil {
		return fmt.Errorf("error while finding update script in configuration: %s", findErr.Error())
	}
	localVersion, err := FindAndExecute(configFolder, configLocalVersion)
	if err != nil {
		return fmt.Errorf("error while checking local version of configuration: %s", err.Error())
	}
	remoteVersion, err := FindAndExecute(configFolder, configRemoteVersion)
	if err != nil {
		return fmt.Errorf("error while checking remote version of configuration: %s", err.Error())
	}
	if localVersion != remoteVersion {
		_, updateErr := OutputOfExecuting(configFolder, updateScriptFile)
		if updateErr != nil {
			return fmt.Errorf("error while updating configuration: %s", err.Error())
		}
	}
	// after updating the configuration, we reload the jobs that exist
	return reloadJobs()
}

func reloadJobs() error {
	existingJobs.Lock()
	defer existingJobs.Unlock()
	versionedJobs.Lock()
	defer versionedJobs.Unlock()
	// TODO: deal with jobs that have disappeared but are still running
	// TODO: deal with new jobs
	// TODO: deal with existing jobs
	return filepath.Walk(configFolder, func(file string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			jobPathConfigSplit := strings.SplitN(path.Dir(file), string(os.PathSeparator), 2)
			if len(jobPathConfigSplit) < 2 {
				return nil
			}
			jobPath := jobPathConfigSplit[1]
			if strings.HasPrefix(jobPath, ".") {
				return nil
			}
			if strings.HasPrefix(info.Name(), string(agentInit)) {
				log.Printf("Found job %s", jobPath)
				existingJobs.jobs = append(existingJobs.jobs, jobPath)
			}
			if strings.HasPrefix(info.Name(), string(versionCheck)) {
				log.Printf("Found versioned job: %s", jobPath)
				versionedJobs.jobs = append(versionedJobs.jobs, jobPath)
			}
		}
		return nil
	})
}

func launchBackgroundUpdater() chan<- struct{} {
	updaterStopEvent := make(chan struct{}, 1)
	// we can "afford" to do this every 30 seconds because our version checking is fairly lightweight
	jobUpdateDelay := 30 * time.Second
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

func jobsToTrigger(jobName string) []string {
	existingJobs.Lock()
	defer existingJobs.Unlock()
	var jobs []string
	jobPrefix := jobName + string(os.PathSeparator)
	for _, validJobName := range existingJobs.jobs {
		if validJobName == jobName || strings.HasPrefix(validJobName, jobPrefix) {
			jobs = append(jobs, validJobName)
		}
	}
	return jobs
}

// FindScriptInJobOrParents looks for a script not only in a job folder but in the entire hierarchy of jobs/job-groups
// and returns the folder and script name where the sought for script is, and a potential error
func FindScriptInJobOrParents(jobName string, scriptPrefix Prefix) (string, FormicaScript, error) {
	scriptLocation := filepath.Join(configFolder, jobName)
	for filepath.Base(scriptLocation) != configFolder {
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

func generateJobRunCode(jobName string) (string, error) {
	jobFolder := filepath.Join(configFolder, jobName)
	// we only run this script if it is found, if not, then no problem
	localScriptOutput := ""
	runLocalScript, findErr := FindScript(jobFolder, generate)
	if findErr == nil {
		var runLocalErr error
		localScriptOutput, runLocalErr = OutputOfExecuting(jobFolder, runLocalScript)
		if runLocalErr != nil {
			return "", fmt.Errorf("error while running %s script: %s", generate, runLocalErr.Error())
		}
	}
	// the run script is transferred and run on the remote machine
	runScript, findErr := FindScript(jobFolder, run)
	if findErr != nil {
		return "", fmt.Errorf("couldn't find %s script for job %s: %s", run, jobName, findErr.Error())
	}
	jobScript, transferErr := TransferAndRunScriptCommand(filepath.Join(jobFolder, string(runScript)), "/tmp/formica_agent")
	if transferErr != nil {
		return "", fmt.Errorf("error while preparing job run script: %s", transferErr.Error())
	}
	// we combine the locally generated shell code + the job transfer/run commands
	jobScript = localScriptOutput + jobScript

	log.Printf("The job script is %s", jobScript)

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

func saveAndExportJobVersion(jobRunFolder, jobName string) (string, error) {
	versionScriptLocation, versionScript, err := FindScriptInJobOrParents(jobName, versionCheck)
	if err != nil {
		return "", fmt.Errorf("error while loading job %s version check script: %s", jobName, err.Error())
	}
	if versionScript != "" {
		jobVersion, err := OutputOfExecuting(versionScriptLocation, versionScript)
		if err != nil {
			return "", fmt.Errorf("error when executing version check script for job %s: %s", jobName, err.Error())
		}
		err = ioutil.WriteFile(filepath.Join(jobRunFolder, versionTag), []byte(jobVersion), 0644)
		if err != nil {
			return "", fmt.Errorf("error when writing version tag for job %s: %s", jobName, err.Error())
		}
		return fmt.Sprintf("export JOB_VERSION='%s'\n", jobVersion), nil
	}
	return "", nil
}

func runJob(jobName string) (*exec.Cmd, error) {
	jobFolder := filepath.Join(configFolder, jobName)
	log.Printf("Launching job from %s", jobFolder)
	agentInitScript, findErr := FindScript(jobFolder, agentInit)
	if findErr != nil {
		return nil, fmt.Errorf("error while searching for %s script: %s", agentInit, findErr.Error())
	}

	jobScript, err := generateJobRunCode(jobName)
	if err != nil {
		return nil, fmt.Errorf("error while preparing job run code: %s", err.Error())
	}
	jobRunFolder, err := prepareRunFolder(jobName)
	if err != nil {
		return nil, fmt.Errorf("error while setting up job run folder: %s", err.Error())
	}
	versionExportScript, err := saveAndExportJobVersion(jobRunFolder, jobName)
	if err != nil {
		return nil, err
	}
	stdoutWriter, err := BuildTimestampedLogger(jobRunFolder, stdoutPrefix)
	if err != nil {
		return nil, err
	}
	stderrWriter, err := BuildTimestampedLogger(jobRunFolder, stderrPrefix)
	if err != nil {
		return nil, err
	}

	runFileEnvVariable := fmt.Sprintf("export FORMICA_RUN='%s'\n", jobRunFolder)
	stdin := strings.NewReader(versionExportScript + runFileEnvVariable + jobScript)
	newJob := PrepareCommand(jobFolder, agentInitScript, stdin, stdoutWriter, stderrWriter)
	newJob.Start()
	go func() {
		defer stdoutWriter.Close()
		defer stderrWriter.Close()
		newJob.Wait()
		agentCleanupScript, findErr := FindScript(jobRunFolder, agentCleanup)
		if findErr != nil {
			log.Printf("error while looking for cleanup script of job '%s': %s", jobName, err.Error())
		}
		_, err := OutputOfExecuting(jobRunFolder, agentCleanupScript)
		if err != nil {
			log.Printf("error while cleaning up agent for job '%s': %s", jobName, err.Error())
		}
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
				jobsToTrigger := jobsToTrigger(jobToRun)
				if len(jobsToTrigger) == 0 {
					log.Printf("no job matching '%s' was found in the configuration folder!", jobToRun)
					continue
				}
				for _, jobToRun := range jobsToTrigger {
					newJob, err := runJob(jobToRun)
					if err != nil {
						log.Printf("error while running job '%s': %s", jobToRun, err.Error())
						continue
					}
					runningJobs = append(runningJobs, *newJob)
				}
			}
		}
	}()
	return jobReceiver, runnerStopEvent
}

func launchJobQueueListener(jobRunner chan<- string) chan<- struct{} {
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
						jobRunner <- jobName
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

func fetchVersionsOfJobs() *map[string]string {
	versionedJobs.Lock()
	currentVersionedJobs := versionedJobs.jobs[:]
	versionedJobs.Unlock()
	versions := make(map[string]*strings.Builder)
	versionCheckStderrs := make(map[string]*strings.Builder)
	versionCmds := make(map[string]*exec.Cmd)
	for _, versionedJob := range currentVersionedJobs {
		versionedJobFolder := filepath.Join(configFolder, versionedJob)
		versionCheckScript, findErr := FindScript(versionedJobFolder, versionCheck)
		if findErr != nil {
			log.Printf("error while searching for %s script: %s", versionCheck, findErr.Error())
		}
		versionStdout := &strings.Builder{}
		versionStderr := &strings.Builder{}
		emptyReader := strings.NewReader("")
		versionCheckCommand := PrepareCommand(versionedJobFolder, versionCheckScript, emptyReader, versionStdout, versionStderr)
		versions[versionedJob] = versionStdout
		versionCheckStderrs[versionedJob] = versionStderr
		versionCmds[versionedJob] = versionCheckCommand
		err := versionCheckCommand.Start()
		if err != nil {
			log.Printf("error while checking version of job %s: %s", versionedJob, err.Error())
		}
	}
	for versionedJob, versionCmd := range versionCmds {
		err := versionCmd.Wait()
		if err != nil {
			log.Printf("error while waiting for version check of %s to finish:\nError message: %s\nstderr output:%s", versionedJob, err.Error(), versionCheckStderrs[versionedJob])
		}
	}
	versionsOfJobs := make(map[string]string)
	for versionedJob, versionBuffer := range versions {
		jobsUnderVersion := jobsToTrigger(versionedJob)
		for _, jobUnderVersion := range jobsUnderVersion {
			log.Printf("Job %s is now at version %s", jobUnderVersion, versionBuffer.String())
			versionsOfJobs[jobUnderVersion] = versionBuffer.String()
		}
	}
	return &versionsOfJobs
}

func getVersionsOfLatestRuns() *map[string]string {
	versionsForRun := make(map[string]string)
	_, err := os.Stat(formicaRuns)
	if os.IsNotExist(err) {
		return &versionsForRun
	}
	if err != nil {
		log.Printf("error while checking %s folder for version tags: %s", formicaRuns, err.Error())
	}
	existingJobs.Lock()
	jobs := existingJobs.jobs[:]
	existingJobs.Unlock()
	for _, job := range jobs {
		jobRuns, err := ioutil.ReadDir(filepath.Join(formicaRuns, job))
		if os.IsNotExist(err) {
			// no run yet, so no version
			continue
		}
		if err != nil {
			log.Printf("error while checking runs of job %s for version tags: %s", job, err.Error())
			continue
		}
		latestJobRun := -1
		for _, jobRun := range jobRuns {
			if !jobRun.IsDir() {
				// job runs are only folders
				continue
			}
			jobRunNumber, err := strconv.Atoi(jobRun.Name())
			if err != nil {
				// non-numeric folders are not job runs
				continue
			}
			if latestJobRun < jobRunNumber {
				latestJobRun = jobRunNumber
			}
		}
		versionTagFile := filepath.Join(formicaRuns, job, strconv.Itoa(latestJobRun), versionTag)
		versionTagOfRun, err := ioutil.ReadFile(versionTagFile)
		if os.IsNotExist(err) {
			// no version tag in the job run means that the job is not versioned
			continue
		}
		if err != nil {
			log.Printf("error while reader %s file: %s", versionTagFile, err.Error())
			continue
		}
		versionsForRun[job] = string(versionTagOfRun)
	}
	return &versionsForRun
}

func setupVersionedJobs(jobRunner chan<- string) chan<- struct{} {
	versionedAutoJobsStopEvent := make(chan struct{}, 1)
	pollDelay := 60 * time.Second

	go func() {
		for {
			select {
			case <-versionedAutoJobsStopEvent:
				return
			case <-time.After(pollDelay):
				versionsOfJobs := *fetchVersionsOfJobs()
				lastSeenVersions := *getVersionsOfLatestRuns()
				for jobName, newVersion := range versionsOfJobs {
					if newVersion == "" {
						log.Printf("Job %s has a blank version!", jobName)
					}
					log.Printf("Versioned job %s was last run with version %s and version %s is available", jobName, lastSeenVersions[jobName], newVersion)
					if newVersion != lastSeenVersions[jobName] {
						jobRunner <- jobName
					}
				}
			}
		}
	}()

	return versionedAutoJobsStopEvent
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
	jobRunner, jobRunnerStop := launchJobRunner()
	jobQueueListenerStop := launchJobQueueListener(jobRunner)
	versionedJobListenerStop := setupVersionedJobs(jobRunner)

	log.Println("Formica CI is now running")

	for {
		select {
		case <-shutdownNotifiers.Slow:
			// notify updater to stop
			updaterStop <- struct{}{}
			jobRunnerStop <- struct{}{}
			jobQueueListenerStop <- struct{}{}
			versionedJobListenerStop <- struct{}{}
			break
		case <-shutdownNotifiers.Immediate:
			break
		case <-shutdownNotifiers.ForceTermination:
			break
		}
	}
}
