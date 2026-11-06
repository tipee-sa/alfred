package jobfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/gammadia/alfred/proto"
	// Fork of github.com/Masterminds/sprig/v3 removing functions having 3rd party dependencies. List available here :
	// https://github.com/go-task/slim-sprig/commit/4aa88173255771335225fa85e97341262276d42b?w=1#diff-5faaf3bff8320883fedc2b5e1828c8cea82467bd7d8357e1c824dafe50835bfb
	"github.com/go-task/slim-sprig/v3"
	"github.com/samber/lo"
	"google.golang.org/protobuf/types/known/durationpb"
	"gopkg.in/yaml.v3"
)

type ReadOptions struct {
	// Use verbose output when reading the jobfile
	Verbose bool
	// Jobfile arguments
	Args []string
	// Jobfile parameters
	Params map[string]string
}

// jobfileDir returns the absolute directory of the jobfile, which its relative paths
// (dockerfile, context) are resolved against, whether file is relative or absolute.
func jobfileDir(file string) string {
	return filepath.Dir(lo.Must(filepath.Abs(file)))
}

type UnmarshalError struct {
	error
	Source string
}

// parseJobfile decodes a rendered jobfile and refuses the keys Jobfile does not have: yaml.v3
// drops them silently otherwise, so a misspelled key quietly kept its default (a service's
// health "wait:" for "interval:" went unnoticed for months).
func parseJobfile(source []byte) (Jobfile, error) {
	var jobfile Jobfile
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	decoder.KnownFields(true)
	if err := decoder.Decode(&jobfile); err != nil && !errors.Is(err, io.EOF) { // EOF: an empty jobfile, which Validate refuses
		return Jobfile{}, err
	}
	return jobfile, nil
}

func Read(file string, options ReadOptions) (job *proto.Job, renderedSource string, err error) {
	job = &proto.Job{}
	workDir := jobfileDir(file)

	var buf []byte
	if buf, err = os.ReadFile(file); err != nil {
		return nil, "", fmt.Errorf("failed to read file: %w", err)
	}

	source, err := evaluateTemplate(string(buf), workDir, options)
	if err != nil {
		return nil, "", fmt.Errorf("failed to evaluate template: %w", err)
	}
	renderedSource = source

	jobfile, err := parseJobfile([]byte(source))
	if err != nil {
		return nil, "", UnmarshalError{fmt.Errorf("failed to unmarshal jobfile: %w", err), source}
	}
	jobfile.path = workDir
	if err = jobfile.Validate(); err != nil {
		return nil, "", UnmarshalError{fmt.Errorf("failed to validate jobfile: %w", err), source}
	}

	job.Name = jobfile.Name
	job.Tasks = lo.Map(jobfile.Tasks, func(t JobfileTask, _ int) *proto.Job_Task {
		return &proto.Job_Task{Name: t.Name, Slots: t.Slots}
	})
	job.Steps = make([]string, len(jobfile.Steps))
	for i, step := range jobfile.Steps {
		if job.Steps[i], err = buildImage(
			path.Join(workDir, step.Dockerfile),
			path.Join(workDir, step.Context),
			step.Options,
			options,
		); err != nil {
			return nil, "", fmt.Errorf("failed to build image for jobfile step (%d): %w", i+1, err)
		}
	}

	if len(jobfile.Env) > 0 {
		job.Env = lo.MapToSlice(jobfile.Env, func(key string, value string) *proto.Job_Env {
			return &proto.Job_Env{
				Key:   key,
				Value: value,
			}
		})
	}
	if len(jobfile.Secrets) > 0 {
		job.Secrets = lo.MapToSlice(jobfile.Secrets, func(key string, value string) *proto.Job_Env {
			return &proto.Job_Env{
				Key:   key,
				Value: value,
			}
		})
	}

	for name, service := range jobfile.Services {
		jobService := &proto.Job_Service{
			Name:    name,
			Image:   service.Image,
			Command: service.Command,
			Tmpfs:   service.Tmpfs,
			Env: lo.MapToSlice(service.Env, func(key string, value string) *proto.Job_Env {
				return &proto.Job_Env{
					Key:   key,
					Value: value,
				}
			}),
			Health: lo.Ternary(len(service.Health.Cmd) < 1, nil, &proto.Job_Service_Health{
				Cmd:  service.Health.Cmd[0],
				Args: service.Health.Cmd[1:],
			}),
		}

		if service.Health.Timeout != "" {
			jobService.Health.Timeout = durationpb.New(lo.Must(time.ParseDuration(service.Health.Timeout)))
		}
		if service.Health.Interval != "" {
			jobService.Health.Interval = durationpb.New(lo.Must(time.ParseDuration(service.Health.Interval)))
		}
		if service.Health.Retries != "" {
			retries := uint32(lo.Must(strconv.ParseUint(service.Health.Retries, 10, 32)))
			jobService.Health.Retries = &retries
		}

		job.Services = append(job.Services, jobService)
	}

	return
}

type TemplateData struct {
	Env    map[string]string
	Args   []string
	Params map[string]string
}

func evaluateTemplate(source string, dir string, options ReadOptions) (string, error) {
	var userError error

	tmpl, err := template.New("jobfile").Funcs(sprig.FuncMap()).Funcs(template.FuncMap{
		// This one is better than sprig's fail() because it produces less noise in the error message.
		"error": func(err string) any {
			userError = errors.New(err)
			panic(nil)
		},
		"exec": func(args ...string) string {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = dir
			if options.Verbose {
				cmd.Stderr = os.Stderr
			}
			output, err := cmd.Output()
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
					panic(fmt.Errorf("%w\n%s", err, strings.TrimRight(string(exitErr.Stderr), "\n")))
				}
				panic(err)
			}
			return strings.TrimRight(string(output), "\n\r")
		},
		"shell": func(script string) string {
			cmd := exec.Command("bash", "-euo", "pipefail", "-c", script)
			cmd.Dir = dir
			if options.Verbose {
				cmd.Stderr = os.Stderr
			}
			output, err := cmd.Output()
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
					panic(fmt.Errorf("%w\n%s", err, strings.TrimRight(string(exitErr.Stderr), "\n")))
				}
				panic(err)
			}
			return strings.TrimRight(string(output), "\n\r")
		},
	}).Parse(source)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	data := TemplateData{
		Env: lo.SliceToMap(
			os.Environ(),
			func(env string) (key, val string) { key, val, _ = strings.Cut(env, "="); return },
		),
		Args:   options.Args,
		Params: options.Params,
	}

	var output strings.Builder
	if err := tmpl.Execute(&output, data); err != nil {
		return "", lo.Ternary(userError != nil, userError, err)
	}

	return output.String(), nil
}

// buildImage builds the main Docker image for the job and returns a tag referencing it.
//
// The image's ID comes from the daemon (docker image inspect of a temporary tag), not from
// --iidfile: without attestations (see buildCommand), the containerd store's --iidfile gives a
// digest the daemon does not know, and tagging it failed with "No such image". The ID itself
// depends on the image store (classic: config digest, containerd: manifest digest), so after a
// docker save | docker load between two daemons using different stores, the loaded image no
// longer has the ID the client saw. Tags survive the round trip on both stores, so the image
// travels under one.
func buildImage(dockerfile string, dir string, buildOptions []string, readOptions ReadOptions) (string, error) {
	// Unique, so that concurrent builds never take each other's image
	buildTag := fmt.Sprintf("alfred-build:%d-%d", os.Getpid(), time.Now().UnixNano())
	args := []string{"build", "--platform", "linux/amd64", ".", "-f", dockerfile}
	args = append(args, buildOptions...)
	args = append(args, "--tag", buildTag)

	cmd := buildCommand(dir, args)
	// The build's output is progress, never the command's output: with -v it goes to stderr,
	// so that stdout stays pure (run --json, run --dry-run). Without -v, the end of it is kept
	// for the error, which otherwise says only "exit status 1".
	var output tailBuffer
	if readOptions.Verbose {
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	} else {
		cmd.Stdout, cmd.Stderr = &output, &output
	}

	if err := cmd.Run(); err != nil {
		if tail := output.String(); tail != "" {
			return "", fmt.Errorf("failed to build image: %w\n%s", err, tail)
		}
		return "", fmt.Errorf("failed to build image: %w", err)
	}
	// Removes only the temporary tag: the image keeps the one below
	defer func() { _ = exec.Command("docker", "image", "rm", buildTag).Run() }()

	out, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", buildTag).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to read the built image's ID (output: %s): %w", strings.TrimSpace(string(out)), err)
	}
	tag := imageTag(strings.TrimSpace(string(out)))
	if out, err := exec.Command("docker", "tag", buildTag, tag).CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to tag image as '%s' (output: %s): %w", tag, strings.TrimSpace(string(out)), err)
	}

	return tag, nil
}

// buildCommand returns the docker build command, without BuildKit's default provenance
// attestation: it records the build's time, and on the containerd image store the index it
// sits in gives the image its ID, so every build of an unchanged image got a new ID. The
// server and every node then took it as a new image (uploaded, archived and loaded again).
// The variable, unlike --provenance=false, is ignored by builders that have no attestations.
func buildCommand(dir string, args []string) *exec.Cmd {
	cmd := exec.Command("docker", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BUILDX_NO_DEFAULT_ATTESTATIONS=1")
	return cmd
}

// tailBuffer keeps the end of what is written to it: at most tailBufferBytes, of which String
// returns the last tailBufferLines lines. The byte limit is what bounds memory: a build can write
// megabytes without a newline, and a line count alone would keep (and re-copy) all of it.
type tailBuffer struct {
	buf       []byte
	truncated bool
}

const (
	tailBufferLines = 20
	tailBufferBytes = 16 << 10
)

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > tailBufferBytes {
		p = p[len(p)-tailBufferBytes:]
		b.truncated = true
	}
	if over := len(b.buf) + len(p) - tailBufferBytes; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
		b.truncated = true
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

func (b *tailBuffer) String() string {
	lines := strings.Split(strings.TrimRight(string(b.buf), "\n"), "\n")
	if len(lines) > tailBufferLines {
		lines = lines[len(lines)-tailBufferLines:]
	} else if b.truncated {
		// The first line lost its beginning to the byte limit.
		lines[0] = "[...]" + lines[0]
	}
	return strings.Join(lines, "\n")
}

// imageTag returns the tag under which the image with the given ID is sent to the server.
// The same content always gets the same tag, which lets the server skip images it already has.
func imageTag(imageId string) string {
	return "alfred-image:" + strings.TrimPrefix(imageId, "sha256:")
}
