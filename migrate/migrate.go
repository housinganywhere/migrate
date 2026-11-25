// Package migrate is imported by other Go code.
// It is the entry point to all migration functions.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/housinganywhere/migrate/driver"
	"github.com/housinganywhere/migrate/file"
	"github.com/housinganywhere/migrate/migrate/direction"
	pipep "github.com/housinganywhere/migrate/pipe"
)

// Up applies all available migrations
func Up(pipe chan interface{}, url, migrationsPath string) {
	r := newRun()
	defer r.stop()
	r.migrate(pipe, url, migrationsPath, (*file.MigrationFiles).ToLastFrom)
	close(pipe)
}

// UpSync is synchronous version of Up
func UpSync(url, migrationsPath string) (err []error, ok bool) {
	pipe := pipep.New()
	go Up(pipe, url, migrationsPath)
	err = pipep.ReadErrors(pipe)
	return err, len(err) == 0
}

// Down rolls back all migrations
func Down(pipe chan interface{}, url, migrationsPath string) {
	r := newRun()
	defer r.stop()
	r.migrate(pipe, url, migrationsPath, (*file.MigrationFiles).ToFirstFrom)
	close(pipe)
}

// DownSync is synchronous version of Down
func DownSync(url, migrationsPath string) (err []error, ok bool) {
	pipe := pipep.New()
	go Down(pipe, url, migrationsPath)
	err = pipep.ReadErrors(pipe)
	return err, len(err) == 0
}

// Redo rolls back the most recently applied migration, then runs it again.
func Redo(pipe chan interface{}, url, migrationsPath string) {
	r := newRun()
	defer r.stop()
	if r.migrate(pipe, url, migrationsPath, relative(-1)) {
		r.migrate(pipe, url, migrationsPath, relative(+1))
	}
	close(pipe)
}

// RedoSync is synchronous version of Redo
func RedoSync(url, migrationsPath string) (err []error, ok bool) {
	pipe := pipep.New()
	go Redo(pipe, url, migrationsPath)
	err = pipep.ReadErrors(pipe)
	return err, len(err) == 0
}

// Reset runs the down and up migration function
func Reset(pipe chan interface{}, url, migrationsPath string) {
	r := newRun()
	defer r.stop()
	if r.migrate(pipe, url, migrationsPath, (*file.MigrationFiles).ToFirstFrom) {
		r.migrate(pipe, url, migrationsPath, (*file.MigrationFiles).ToLastFrom)
	}
	close(pipe)
}

// ResetSync is synchronous version of Reset
func ResetSync(url, migrationsPath string) (err []error, ok bool) {
	pipe := pipep.New()
	go Reset(pipe, url, migrationsPath)
	err = pipep.ReadErrors(pipe)
	return err, len(err) == 0
}

// Migrate applies relative +n/-n migrations
func Migrate(pipe chan interface{}, url, migrationsPath string, relativeN int) {
	r := newRun()
	defer r.stop()
	r.migrate(pipe, url, migrationsPath, relative(relativeN))
	close(pipe)
}

// MigrateSync is synchronous version of Migrate
func MigrateSync(url, migrationsPath string, relativeN int) (err []error, ok bool) {
	pipe := pipep.New()
	go Migrate(pipe, url, migrationsPath, relativeN)
	err = pipep.ReadErrors(pipe)
	return err, len(err) == 0
}

// Version returns the current migration version
func Version(url, migrationsPath string) (version uint64, err error) {
	d, err := driver.New(url)
	if err != nil {
		return 0, err
	}
	defer func() {
		err = d.Close()
	}()
	return d.Version()
}

// Create creates new migration files on disk
func Create(url, migrationsPath, name string) (*file.MigrationFile, error) {
	ext, err := driver.FilenameExtensionFromURL(url)
	if err != nil {
		return nil, err
	}

	files, err := file.ReadMigrationFiles(migrationsPath, file.FilenameRegex(ext))
	if err != nil {
		return nil, err
	}

	version := uint64(time.Now().Unix())

	for _, f := range files {
		if f.Version == version {
			version++
		}
	}

	versionStr := strconv.FormatUint(version, 10)

	length := 10
	if len(versionStr)%length != 0 {
		versionStr = strings.Repeat("0", length-len(versionStr)%length) + versionStr
	}

	filenamef := "%s_%s.%s.%s"
	name = strings.Replace(name, " ", "_", -1)

	mfile := &file.MigrationFile{
		Version: version,
		UpFile: &file.File{
			Path:      migrationsPath,
			FileName:  fmt.Sprintf(filenamef, versionStr, name, "up", ext),
			Name:      name,
			Content:   []byte(""),
			Direction: direction.Up,
		},
		DownFile: &file.File{
			Path:      migrationsPath,
			FileName:  fmt.Sprintf(filenamef, versionStr, name, "down", ext),
			Name:      name,
			Content:   []byte(""),
			Direction: direction.Down,
		},
	}

	if err := ioutil.WriteFile(path.Join(mfile.UpFile.Path, mfile.UpFile.FileName), mfile.UpFile.Content, 0644); err != nil {
		return nil, err
	}
	if err := ioutil.WriteFile(path.Join(mfile.DownFile.Path, mfile.DownFile.FileName), mfile.DownFile.Content, 0644); err != nil {
		return nil, err
	}

	return mfile, nil
}

// initDriverAndReadMigrationFilesAndGetVersion is a small helper
// function that is common to most of the migration funcs
func initDriverAndReadMigrationFilesAndGetVersion(url, migrationsPath string) (driver.Driver, *file.MigrationFiles, uint64, error) {
	d, err := driver.New(url)
	if err != nil {
		return nil, nil, 0, err
	}
	files, err := file.ReadMigrationFiles(migrationsPath, file.FilenameRegex(d.FilenameExtension()))
	if err != nil {
		d.Close() // TODO what happens with errors from this func?
		return nil, nil, 0, err
	}
	version, err := d.Version()
	if err != nil {
		d.Close() // TODO what happens with errors from this func?
		return nil, nil, 0, err
	}
	return d, &files, version, nil
}

// NewPipe is a convenience function for pipe.New().
// This is helpful if the user just wants to import this package and nothing else.
func NewPipe() chan interface{} {
	return pipep.New()
}

// interrupts is an internal variable that holds the state of
// interrupt handling
var interrupts = true

// Graceful enables signal handling (the default). On SIGINT (^C) or
// SIGTERM (sent by Kubernetes/Docker when stopping a container) the running
// migration is cancelled on the database, so it is rolled back, and no
// further migrations are run. A second signal exits immediately.
//
// Drivers that cannot cancel a running migration exit immediately.
func Graceful() {
	interrupts = true
}

// NonGraceful disables signal handling. Signals get Go's default behaviour,
// i.e. the process is killed without cancelling the running migration.
func NonGraceful() {
	interrupts = false
}

// errAborted is reported when a run stops early because of a signal.
var errAborted = errors.New("aborted by signal: remaining migrations were not applied")

// selectFiles picks the migration files to apply from the current version.
type selectFiles func(files *file.MigrationFiles, version uint64) (file.Files, error)

func relative(n int) selectFiles {
	return func(files *file.MigrationFiles, version uint64) (file.Files, error) {
		return files.From(version, n)
	}
}

// run holds the signal state of one top-level operation (Up, Down, Redo,
// ...), so signals are counted across every migration file it applies.
type run struct {
	signals  chan os.Signal // nil when interrupts are disabled
	stopping bool           // a signal was received: run nothing further
}

func newRun() *run {
	r := &run{}
	if interrupts {
		r.signals = make(chan os.Signal, 4)
		signal.Notify(r.signals, os.Interrupt, syscall.SIGTERM)
	}
	return r
}

func (r *run) stop() {
	if r.signals != nil {
		signal.Stop(r.signals)
	}
}

// handle stops the run and cancels the running migration. cancel is nil
// when no migration is running. A second signal exits immediately.
func (r *run) handle(sig os.Signal, cancel func(), pipe chan interface{}) {
	if r.stopping {
		os.Exit(5)
	}
	r.stopping = true
	if cancel != nil {
		cancel()
		// add white space at beginning for ^C splitting
		pipe <- fmt.Sprintf(" %s received, cancelling running migration ... Send again to force quit.", signalName(sig))
	}
}

// poll handles signals received while no migration was running.
func (r *run) poll(pipe chan interface{}) {
	for {
		select {
		case sig := <-r.signals:
			r.handle(sig, nil, pipe)
		default:
			return
		}
	}
}

func (r *run) migrate(pipe chan interface{}, url, migrationsPath string, sel selectFiles) (ok bool) {
	if r.poll(pipe); r.stopping {
		pipe <- errAborted
		return false
	}

	d, files, version, err := initDriverAndReadMigrationFilesAndGetVersion(url, migrationsPath)
	if err != nil {
		pipe <- err
		return false
	}
	defer func() {
		if err := d.Close(); err != nil {
			pipe <- err
			ok = false
		}
	}()

	applyMigrationFiles, err := sel(files, version)
	if err != nil {
		pipe <- err
		return false
	}

	for _, f := range applyMigrationFiles {
		if r.poll(pipe); r.stopping {
			pipe <- errAborted
			return false
		}
		if !r.apply(d, f, pipe) {
			return false
		}
	}
	return true
}

// apply runs a single migration file and redirects its output to pipe,
// handling signals while it runs. It returns false if the migration failed.
func (r *run) apply(d driver.Driver, f file.File, pipe chan interface{}) bool {
	filePipe := pipep.New()
	var cancel func()
	if cd, ok := d.(driver.ContextDriver); ok {
		ctx, cancelCtx := context.WithCancel(context.Background())
		defer cancelCtx()
		cancel = cancelCtx
		go cd.MigrateContext(ctx, f, filePipe)
	} else {
		cancel = func() { os.Exit(5) }
		go d.Migrate(f, filePipe)
	}

	failed := false
	for {
		select {
		case sig := <-r.signals:
			r.handle(sig, cancel, pipe)
		case item, more := <-filePipe:
			if !more {
				return !failed
			}
			pipe <- item
			if _, isErr := item.(error); isErr {
				failed = true
			}
		}
	}
}

func signalName(sig os.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case os.Interrupt:
		return "SIGINT"
	}
	return sig.String()
}
