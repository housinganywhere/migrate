// Package pipe has functions for pipe channel handling.
package pipe

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/fatih/color"
	"github.com/housinganywhere/migrate/file"
	"github.com/housinganywhere/migrate/migrate/direction"
)

// New creates a new pipe. A pipe is basically a channel.
func New() chan interface{} {
	return make(chan interface{}, 0)
}

// Close closes a pipe and optionally sends an error
func Close(pipe chan interface{}, err error) {
	if err != nil {
		pipe <- err
	}
	close(pipe)
}

// WaitAndRedirect waits for pipe to be closed and
// redirects all messages from pipe to redirectPipe
// while it waits. It also checks if there was an
// interrupt send and will quit gracefully if yes.
func WaitAndRedirect(pipe, redirectPipe chan interface{}, interrupt chan os.Signal) (ok bool) {
	errorReceived := false
	interruptsReceived := 0
	defer stopNotifyInterruptChannel(interrupt)
	if pipe != nil && redirectPipe != nil {
		for {
			select {

			case <-interrupt:
				interruptsReceived++
				if interruptsReceived > 1 {
					os.Exit(5)
				} else {
					// add white space at beginning for ^C splitting
					redirectPipe <- " Aborting after this migration ... Hit again to force quit."
				}

			case item, ok := <-pipe:
				if !ok {
					return !errorReceived && interruptsReceived == 0
				}
				redirectPipe <- item
				switch item.(type) {
				case error:
					errorReceived = true
				}
			}
		}
	}
	return !errorReceived && interruptsReceived == 0
}

// ReadErrors selects all received errors and returns them.
// This is helpful for synchronous migration functions.
func ReadErrors(pipe chan interface{}) []error {
	err := make([]error, 0)
	if pipe != nil {
		for {
			select {
			case item, ok := <-pipe:
				if !ok {
					return err
				}
				switch item.(type) {
				case error:
					err = append(err, item.(error))
				}
			}
		}
	}
	return err
}

func stopNotifyInterruptChannel(interruptChannel chan os.Signal) {
	if interruptChannel != nil {
		signal.Stop(interruptChannel)
	}
}

func WritePipe(pipe chan interface{}) (ok bool) {
	okFlag := true
	if pipe != nil {
		for {
			select {
			case item, more := <-pipe:
				if !more {
					return okFlag
				}
				switch item.(type) {

				case string:
					fmt.Println(item.(string))

				case error:
					c := color.New(color.FgRed)
					c.Printf("%s\n\n", item.(error).Error())
					okFlag = false

				case file.File:
					f := item.(file.File)
					if f.Direction == direction.Up {
						c := color.New(color.FgGreen)
						c.Print(">")
					} else if f.Direction == direction.Down {
						c := color.New(color.FgRed)
						c.Print("<")
					}
					fmt.Printf(" %s\n", f.FileName)

				default:
					text := fmt.Sprint(item)
					fmt.Println(text)
				}
			}
		}
	}
	return okFlag
}
