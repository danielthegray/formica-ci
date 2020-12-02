package runner

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"time"
)

const configFolder = "formica_conf"
const configInitPrefix = "config_init"
const updatePrefix = "update"
const jobQueue = "job_queue"

// ShutdownNotifiers is a collection of channels used to notify of different shutdown events
type ShutdownNotifiers struct {
	Slow             <-chan struct{}
	Immediate        <-chan struct{}
	ForceTermination <-chan struct{}
}

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
	currentDir, err := os.Getwd()
	if err != nil {
		log.Println("Error when getting the current dir")
		return err
	}
	updateScriptFile, err := FindScript(configFolder, updatePrefix)
	if err != nil {
		return fmt.Errorf("error while finding update script in configuration: %s", err.Error())
	}
	_, err = ExecuteScript(path.Join(currentDir, configFolder), updateScriptFile)
	if err != nil {
		return fmt.Errorf("error while updating configuration: %s", err.Error())
	}
	// after updating the configuration, we reload the jobs that exist
	return reloadJobs()
}

func reloadJobs() error {
	// TODO: deal with jobs that have disappeared but are still running
	// TODO: deal with new jobs
	// TODO: deal with existing jobs
	return nil
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

func launchJobQueueListener() chan<- struct{} {
	jobQueueStopEvent := make(chan struct{}, 1)
	_ = os.Mkdir(jobQueue, 0777)
	queuePollDelay := 1 * time.Second
	go func() {
		for {
			select {
			case <-jobQueueStopEvent:
				// todo remove job queue folder? or nah
				return
			case <-time.After(queuePollDelay):
				filesInQueue, err := ioutil.ReadDir(jobQueue)
				if err != nil {
					log.Printf("error when listing jobs in queue: %s", err.Error())
					continue
				}
				for _, enqueuedJob := range filesInQueue {
					fullJobFilename := path.Join(jobQueue, enqueuedJob.Name())
					jobName, err := ioutil.ReadFile(fullJobFilename)
					if err != nil {
						log.Printf("error while opening job file %s: %s", fullJobFilename, err.Error())
					} else {
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
	jobQueueListenerStop := launchJobQueueListener()

	log.Println("Formica CI is now running")

	for {
		select {
		case <-shutdownNotifiers.Slow:
			// notify updater to stop
			updaterStop <- struct{}{}
			jobQueueListenerStop <- struct{}{}
			break
		case <-shutdownNotifiers.Immediate:
			break
		case <-shutdownNotifiers.ForceTermination:
			break
		}
	}
}
