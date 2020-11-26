package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"danielgray.xyz/formica/runner"
)

func handlersCtrlC() *runner.ShutdownNotifiers {
	slowShutdown := make(chan struct{}, 1)
	immediateShutdown := make(chan struct{}, 1)
	forceTerm := make(chan struct{}, 1)

	ctrlCchan := make(chan os.Signal, 4)
	signal.Notify(ctrlCchan, syscall.SIGINT)
	go func() {
		numberOfCtrlCPresses := 0
		for {
			<-ctrlCchan
			numberOfCtrlCPresses++
			switch numberOfCtrlCPresses {
			case 1:
				log.Println("Starting slow shutdown: No more jobs will be accepted...")
				slowShutdown <- struct{}{}
			case 2:
				log.Println("Triggering immediate shutdown: Cleaning up agents...")
				immediateShutdown <- struct{}{}
			case 3:
				log.Println("Forcing termination of all worker tracker processes. Pressing Ctrl+C again may leave zombie processes!")
				forceTerm <- struct{}{}
			default:
				log.Println("Terminating immediately! (zombie processes may be left, please restart this machine to clear them up)")
				os.Exit(75) // TEMPFAIL
			}
		}
	}()
	return &runner.ShutdownNotifiers{
		Slow:             slowShutdown,
		Immediate:        immediateShutdown,
		ForceTermination: forceTerm,
	}
}

func explainExitLogic() {
	log.Println("Successive Ctrl + C presses will exit, in the following way:")
	log.Println("Press Ctrl + C again to start a slow shutdown: no new jobs will be accepted, but the existing ones will run their course and only then will Formica shutdown.")
	log.Println("Press Ctrl + C again to start an immediate shutdown: all jobs will be terminated and the agent machines cleaned up.")
	log.Println("Press Ctrl + C again to force termination of all agent tracker processes: all the trackers will be terminated without cleaning up the agent machines (at your own risk!)")
	log.Println("Press Ctrl + C one more time to exit immediately (very much at your own risk, zombie processes may be left running on the machine).")
}

func main() {
	shutdownNotifiers := handlersCtrlC()
	//stdin := strings.NewReader("ls\n")
	//stdout := strings.Builder{}
	//stderr := strings.Builder{}
	//runner.PrepareCommand(".", "test", stdin, stdout, stderr)
	explainExitLogic()
	go runner.Start(shutdownNotifiers)
	<-shutdownNotifiers.ForceTermination
	/*
		fmt.Println("Listing the remote server")
		cmd := exec.Command("ssh", "tor-bridge", "ls", "/")
		output, err := cmd.Output()
		if err != nil {
			fmt.Println("There was an error connecting the stdout pipe " + err.Error())
		}
		//err = cmd.Start()
		//if err != nil {
		//fmt.Println("THERE WAS AN ERROR when starting the command! " + err.Error())
		//}
		//err = cmd.Wait()
		//if err != nil {
		//fmt.Println("There was an error while reaping the process")
		//}
		fmt.Println("The command output was:")
		fmt.Println(string(output))
	*/
}
