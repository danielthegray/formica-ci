package runner

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const jobQueue = "job_queue"
const formicaRuns = "formica_runs"
const versionTag = "version.tag"

const agentInit Prefix = "agent_init"
const agentCleanup Prefix = "agent_cleanup"
const singleVersionCheck Prefix = "single_version_check"
const branchedVersionCheck Prefix = "branched_version_check"
const run Prefix = "run"
const generate Prefix = "gen"

const stdoutPrefix = "stdout"
const stderrPrefix = "stderr"

var jobConfig *jobDefinitions

// ShutdownNotifiers is a collection of channels used to notify of different shutdown events
type ShutdownNotifiers struct {
	Slow             <-chan struct{}
	Immediate        <-chan struct{}
	ForceTermination <-chan struct{}
}

func cloneStringBuilder(buf strings.Builder) strings.Builder {
	clonedBuf := strings.Builder{}
	clonedBuf.WriteString(buf.String())
	return clonedBuf
}

type branchState struct {
	Name    string
	Version string
}

func checkBranchVersions(jobPath string) ([]branchState, error) {
	log.WithField("job_path", jobPath).Info("Checking job branch versions")
	branchVersionsOutput, err := FindAndExecute(jobPath, branchedVersionCheck)
	if err != nil {
		return nil, fmt.Errorf("error while checking branch versions: %s", err.Error())
	}
	return parseBranchVersions(branchVersionsOutput), nil
}

func parseBranchVersions(branchVersionCheckOutput string) []branchState {
	branchLines := strings.Split(branchVersionCheckOutput, "\n")
	branchStates := make([]branchState, len(branchLines))
	branchVersionParser := regexp.MustCompile("^([^[[:space:]]]+)[[:space:]]+([[:space:]]+)[[:space:]]*$")
	for _, versionLine := range branchLines {
		parsedLine := branchVersionParser.FindStringSubmatch(versionLine)
		branchVersion := parsedLine[1]
		branchName := parsedLine[2]
		log.WithFields(log.Fields{
			"name":    branchName,
			"version": branchVersion,
		}).Debug("Found branch")
		branchStates = append(branchStates, branchState{
			Name:    branchName,
			Version: branchVersion,
		})
	}
	return branchStates
}

func listBranchesOfJob(jobPath string) ([]string, error) {
	log.WithField("job_path", jobPath).Debug("Listing all branches of branched job")
	branches, err := checkBranchVersions(jobPath)
	if err != nil {
		return nil, err
	}
	branchNames := make([]string, len(branches))
	for _, branch := range branches {
		branchNames = append(branchNames, branch.Name)
	}
	return branchNames, nil
}

// generateAllJobs will generate a list of jobs which will use the configuration at the specified jobPath.
// This can be the case when there is a branching point at some point in the direct parent hierarchy.
// If there is a list of branches defined for one of the parent levels, one job for each branch found
// will be generated, all sharing the same configuration folder.
// The first parameter is the path where the job configuration we are checking is located.
// The second parameters is a map showing the list of branches found at a specific folder in the job definition tree.
func generateAllJobs(jobDefPath string, branchPoints *map[string][]string) ([]jobDef, error) {
	log.WithField("job_def_path", jobDefPath).Info("Generating jobs")
	jobDefHierarchy := strings.Split(jobDefPath, string(os.PathSeparator))
	if len(jobDefHierarchy) < 1 {
		return nil, fmt.Errorf("path of job %s could not be split", jobDefPath)
	}
	// we will store here the final list of jobs found after traversing the whole path
	// we have a list of string builders because we need to dynamically add the branch as
	// an element of the job path where a branch point is found
	jobList := make([]strings.Builder, 1)
	jobList = append(jobList, strings.Builder{})
	currentJobDef := strings.Builder{}
	for _, jobFolder := range jobDefHierarchy {
		if currentJobDef.Len() > 0 {
			currentJobDef.WriteRune(os.PathSeparator)
		}
		currentJobDef.WriteString(jobFolder)
		for _, jobInList := range jobList {
			if jobInList.Len() > 0 {
				jobInList.WriteRune(os.PathSeparator)
			}
			jobInList.WriteString(jobFolder)
		}
		jobPath := currentJobDef.String()
		if (*branchPoints)[jobPath] != nil {
			// the current folder is a branch point (contains a branched version checker script)
			branches := (*branchPoints)[jobPath]
			newJobList := make([]strings.Builder, len(jobList)*len(branches))
			for _, oldJob := range jobList {
				for _, branch := range branches {
					// we clone the old one several times because each new job entry needs its own string builder
					newJobName := cloneStringBuilder(oldJob)
					newJobName.WriteRune(os.PathSeparator)
					newJobName.WriteString(branch)
					newJobList = append(newJobList, newJobName)
				}
			}
			jobList = newJobList
		}
	}
	jobs := make([]jobDef, len(jobList))
	for _, jobName := range jobList {
		log.Infof("Found job %s defined at %s", jobName.String(), jobDefPath)
		jobs = append(jobs, jobDef{
			jobConfigPath: jobDefPath,
			jobName:       jobName.String(),
		})
	}
	return jobs, nil
}

func validateNoNestedBranches(branchPoints *map[string][]string, newBranchPoint string) error {
	for existingBranchPoint := range *branchPoints {
		if strings.HasPrefix(newBranchPoint, existingBranchPoint) {
			return fmt.Errorf("no job branches allowed out of existing branched jobs")
		}
	}
	return nil
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
				newJobConfig, err := updateConfig()
				if err != nil {
					log.Errorf("Unexpected error while keeping configuration updated: %s", err)
				}
				jobConfig = newJobConfig
			}
		}
	}()
	return updaterStopEvent
}

func jobsToTrigger(jobName string) []jobDef {
	log.WithField("job_root", jobName).Info("Triggering job tree")
	var jobs []jobDef
	if jobConfig == nil {
		return jobs
	}
	jobPrefix := jobName + string(os.PathSeparator)
	for _, validJob := range jobConfig.allJobs {
		if validJob.jobName == jobName || strings.HasPrefix(validJob.jobName, jobPrefix) {
			log.WithField("job_name", validJob.jobName).Info("Triggering job")
			jobs = append(jobs, validJob)
		}
	}
	return jobs
}

func generateJobRunCode(job jobDef) (string, error) {
	jobConfFolder := filepath.Join(configFolder, job.jobConfigPath)
	// we only run this script if it is found, if not, then no problem
	localScriptOutput := ""
	runLocalScript, findErr := FindScript(jobConfFolder, generate)
	if findErr == nil {
		var runLocalErr error
		localScriptOutput, runLocalErr = OutputOfExecuting(jobConfFolder, runLocalScript)
		if runLocalErr != nil {
			return "", fmt.Errorf("error while running %s script: %s", generate, runLocalErr.Error())
		}
	}
	// the run script is transferred and run on the remote machine
	runScript, findErr := FindScript(jobConfFolder, run)
	if findErr != nil {
		return "", fmt.Errorf("couldn't find %s script for job %s: %s", run, job.jobName, findErr.Error())
	}
	jobScript, transferErr := TransferAndRunScriptCommand(filepath.Join(jobConfFolder, string(runScript)), "/tmp/formica_agent")
	if transferErr != nil {
		return "", fmt.Errorf("error while preparing job run script: %s", transferErr.Error())
	}
	// we combine the locally generated shell code + the job transfer/run commands
	jobScript = localScriptOutput + jobScript

	log.WithField("job_script", jobScript).Debug("Running job with code")

	return jobScript, nil
}

func prepareRunFolder(job jobDef) (string, error) {
	// the run folder will follow the job name path, to include the branch name
	runFolder := filepath.Join(formicaRuns, job.jobName)
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
			log.Errorf("Error while preparing run folder %s: %s", runFolder, err.Error())
		}
		attempts++
	}
	return runFolder, nil
}

// saveAndExportJobVersion will write the version tag file, and also export a shell environment variable
// that can be read from the job scripts themselves.
func saveAndExportJobVersion(jobRunFolder string, job jobDef) (string, error) {
	singleVersionScriptLocation, singleVersionScript, err := FindScriptInJobOrParents(configFolder, job.jobConfigPath, singleVersionCheck)
	if err != nil {
		return "", fmt.Errorf("error while looking for job %s single version check script: %s", job.jobName, err.Error())
	}
	if singleVersionScript != "" {
		jobVersion, err := OutputOfExecuting(singleVersionScriptLocation, singleVersionScript)
		if err != nil {
			return "", fmt.Errorf("error when executing version check script for job %s: %s", job.jobName, err.Error())
		}
		// write out version.tag file
		err = ioutil.WriteFile(filepath.Join(jobRunFolder, versionTag), []byte(jobVersion), 0644)
		if err != nil {
			return "", fmt.Errorf("error when writing version tag for job %s: %s", job.jobName, err.Error())
		}
		// return job version for usage in other scripts
		return fmt.Sprintf("export JOB_VERSION='%s'\n", jobVersion), nil
	}
	branchedVersionScriptLocation, branchedVersionScript, err := FindScriptInJobOrParents(configFolder, job.jobConfigPath, branchedVersionCheck)
	if err != nil {
		return "", fmt.Errorf("error while looking for job %s branched version check script: %s", job.jobName, err.Error())
	}
	if branchedVersionScript != "" {
		branchedVersionsOutput, err := OutputOfExecuting(branchedVersionScriptLocation, branchedVersionScript)
		if err != nil {
			return "", fmt.Errorf("error when executing version check script for job %s: %s", job.jobName, err.Error())
		}
		versionsAndBranches := parseBranchVersions(branchedVersionsOutput)
		jobVersion := ""
		for _, branchState := range versionsAndBranches {
			if branchState.Name == job.branchName {
				jobVersion = branchState.Version
				break
			}
		}
		if jobVersion == "" {
			return "", fmt.Errorf("no version found for branch %s", job.branchName)
		}
		err = ioutil.WriteFile(filepath.Join(jobRunFolder, versionTag), []byte(jobVersion), 0644)
		if err != nil {
			return "", fmt.Errorf("error when writing version tag for job %s: %s", job.jobName, err.Error())
		}
		return fmt.Sprintf("export JOB_VERSION='%s'\n", jobVersion), nil
	}

	return "", nil
}

func runJob(job jobDef) (*exec.Cmd, error) {
	jobFolder := filepath.Join(configFolder, job.jobName)
	log.WithField("job_folder", jobFolder).Info("Launching job")
	agentInitScript, findErr := FindScript(jobFolder, agentInit)
	if findErr != nil {
		return nil, fmt.Errorf("error while searching for %s script: %s", agentInit, findErr.Error())
	}

	jobScript, err := generateJobRunCode(job)
	if err != nil {
		return nil, fmt.Errorf("error while preparing job run code: %s", err.Error())
	}
	jobRunFolder, err := prepareRunFolder(job)
	if err != nil {
		return nil, fmt.Errorf("error while setting up job run folder: %s", err.Error())
	}
	versionExportScript, err := saveAndExportJobVersion(jobRunFolder, job)
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
			log.Errorf("error while looking for cleanup script of job '%s': %s", job.jobName, err.Error())
		}
		_, err := OutputOfExecuting(jobRunFolder, agentCleanupScript)
		if err != nil {
			log.Errorf("error while cleaning up agent for job '%s': %s", job.jobName, err.Error())
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
					log.Warnf("no job matching '%s' was found in the configuration folder!", jobToRun)
					continue
				}
				for _, jobToRun := range jobsToTrigger {
					newJob, err := runJob(jobToRun)
					if err != nil {
						log.WithField("job_name", jobToRun.jobName).Errorf("error while running job: %s", err.Error())
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
					log.Errorf("error when cleaning up job queue folder: %s", err.Error())
				}
				return
			case <-time.After(queuePollDelay):
				filesInQueue, err := ioutil.ReadDir(jobQueue)
				if err != nil {
					log.Errorf("error when listing jobs in queue: %s", err.Error())
					continue
				}
				for _, enqueuedJob := range filesInQueue {
					fullJobFilename := path.Join(jobQueue, enqueuedJob.Name())
					specifiedJob, err := ioutil.ReadFile(fullJobFilename)
					if err != nil {
						log.Errorf("error while opening job file %s: %s", fullJobFilename, err.Error())
					} else {
						jobName := strings.TrimSpace(string(specifiedJob))
						jobRunner <- jobName
						log.Infof("Found job %s in %s", jobName, fullJobFilename)
					}
					err = os.Remove(fullJobFilename)
					if err != nil {
						log.Errorf("failed to cleanup job file %s: %s", fullJobFilename, err.Error())
					}
				}
			}
		}
	}()
	return jobQueueStopEvent
}

func fetchVersionsOfJobs() *map[string]string {
	log.Info("Checking if versioned jobs have any updates")
	singleVersionCheckers := jobConfig.singleVersionCheckers[:]
	branchedVersionCheckers := jobConfig.branchedVersionCheckers[:]
	singleVersionCmds := make(map[string]*exec.Cmd)
	singleVersionStdouts := make(map[string]*strings.Builder)
	singleVersionStderrs := make(map[string]*strings.Builder)
	for _, singleVersionChecker := range singleVersionCheckers {
		jobFolder := singleVersionChecker.jobFolder
		versionCheckCommand, vcStdout, vcStderr := PrepareCommandWithoutInput(jobFolder, singleVersionChecker.script)
		singleVersionCmds[jobFolder] = versionCheckCommand
		singleVersionStdouts[jobFolder] = vcStdout
		singleVersionStderrs[jobFolder] = vcStderr
		err := versionCheckCommand.Start()
		if err != nil {
			log.Errorf("error while checking version of job %s: %s", jobFolder, err.Error())
		}
	}
	branchedVersionCmds := make(map[string]*exec.Cmd)
	branchedVersionStdouts := make(map[string]*strings.Builder)
	branchedVersionStderrs := make(map[string]*strings.Builder)
	for _, branchedVersionChecker := range branchedVersionCheckers {
		jobFolder := branchedVersionChecker.jobFolder
		versionCheckCommand, vcStdout, vcStderr := PrepareCommandWithoutInput(jobFolder, branchedVersionChecker.script)
		branchedVersionCmds[jobFolder] = versionCheckCommand
		branchedVersionStdouts[jobFolder] = vcStdout
		branchedVersionStderrs[jobFolder] = vcStderr
		err := versionCheckCommand.Start()
		if err != nil {
			log.Errorf("error while checking version of job %s: %s", jobFolder, err.Error())
		}
	}
	for versionedJob, versionCmd := range singleVersionCmds {
		err := versionCmd.Wait()
		if err != nil {
			log.Errorf("error while executing version check of %s:\nError message: %s\nstderr output:%s", versionedJob, err.Error(), singleVersionStderrs[versionedJob])
		}
	}
	for versionedJob, versionCmd := range branchedVersionCmds {
		err := versionCmd.Wait()
		if err != nil {
			log.Errorf("error while executing branched version check of %s:\nError message: %s\nstderr output:%s", versionedJob, err.Error(), branchedVersionStderrs[versionedJob])
		}
	}
	versionsOfJobs := make(map[string]string)
	for versionedJob, versionBuffer := range singleVersionStdouts {
		currentVersion := versionBuffer.String()
		jobsUnderVersion := jobsToTrigger(versionedJob)
		for _, jobUnderVersion := range jobsUnderVersion {
			log.Debugf("Job %s is now at version %s", jobUnderVersion, currentVersion)
			versionsOfJobs[jobUnderVersion.jobName] = currentVersion
		}
	}
	for versionedJob, versionBuffer := range branchedVersionStdouts {
		versionsAndBranches := parseBranchVersions((*versionBuffer).String())
		for _, versionAndBranch := range versionsAndBranches {
			jobWithBranch := versionedJob + versionAndBranch.Name
			versionOfBranch := versionAndBranch.Version
			jobsUnderVersion := jobsToTrigger(jobWithBranch)
			for _, jobUnderVersion := range jobsUnderVersion {
				log.Debugf("Job %s is now at version %s", jobUnderVersion, versionOfBranch)
				versionsOfJobs[jobUnderVersion.jobName] = versionOfBranch
			}
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
		log.Errorf("error while checking %s folder for version tags: %s", formicaRuns, err.Error())
	}
	jobs := jobConfig.allJobs[:]
	for _, job := range jobs {
		jobRuns, err := ioutil.ReadDir(filepath.Join(formicaRuns, job.jobName))
		if os.IsNotExist(err) {
			log.Debugf("job %s has never been run", job.jobName)
			// no run yet, so no version
			continue
		}
		if err != nil {
			log.Errorf("error while checking runs of job %s for version tags: %s", job, err.Error())
			continue
		}
		latestJobRun := -1
		for _, jobRunFolder := range jobRuns {
			if !jobRunFolder.IsDir() {
				// job runs are only folders
				continue
			}
			jobRunNumber, err := strconv.Atoi(jobRunFolder.Name())
			if err != nil {
				// non-numeric folders are not job runs
				continue
			}
			if latestJobRun < jobRunNumber {
				latestJobRun = jobRunNumber
			}
		}
		versionTagFile := filepath.Join(formicaRuns, job.jobName, strconv.Itoa(latestJobRun), versionTag)
		versionTagOfRun, err := ioutil.ReadFile(versionTagFile)
		if os.IsNotExist(err) {
			// no version tag in the job run means that the job is not versioned
			continue
		}
		if err != nil {
			log.Errorf("error while reader %s file: %s", versionTagFile, err.Error())
			continue
		}
		versionsForRun[job.jobName] = string(versionTagOfRun)
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
						log.Warnf("Job %s has a blank version!", jobName)
					}
					log.Debugf("Versioned job %s was last run with version %s and version %s is available", jobName, lastSeenVersions[jobName], newVersion)
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
	currentDir := currentDir()
	err := fetchConfigIfNotPresent(currentDir)
	if err != nil {
		log.Fatalf("%s", err)
	}
	jobConfig, err = updateConfig()
	if err != nil {
		log.Fatalf("configuration is not updateable: %s", err)
	}
	updaterStop := launchBackgroundUpdater()
	jobRunner, jobRunnerStop := launchJobRunner()
	jobQueueListenerStop := launchJobQueueListener(jobRunner)
	versionedJobListenerStop := setupVersionedJobs(jobRunner)

	log.Info("Formica CI is now running")

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

// TODO test that nested branches throw error
