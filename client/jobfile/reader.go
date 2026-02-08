package jobfile

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
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

type UnmarshalError struct {
	error
	Source string
}

func Read(file string, options ReadOptions) (job *proto.Job, renderedSource string, err error) {
	job = &proto.Job{}
	workDir := path.Join(lo.Must(os.Getwd()), path.Dir(file))

	var buf []byte
	if buf, err = os.ReadFile(file); err != nil {
		return nil, "", fmt.Errorf("failed to read file: %w", err)
	}

	source, err := evaluateTemplate(string(buf), workDir, options)
	if err != nil {
		return nil, "", fmt.Errorf("failed to evaluate template: %w", err)
	}
	renderedSource = source

	var jobfile Jobfile
	if err = yaml.Unmarshal([]byte(source), &jobfile); err != nil {
		return nil, "", UnmarshalError{fmt.Errorf("failed to unmarshal jobfile: %w", err), source}
	}
	jobfile.path = workDir
	if err = jobfile.Validate(); err != nil {
		return nil, "", UnmarshalError{fmt.Errorf("failed to validate jobfile: %w", err), source}
	}

	job.Name = jobfile.Name
	job.Tasks = jobfile.Tasks
	job.Flavor = jobfile.Flavor
	job.TasksPerNode = uint32(jobfile.TasksPerNode)
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
			if output, err := cmd.Output(); err != nil {
				panic(err)
			} else {
				return strings.TrimRight(string(output), "\n\r")
			}
		},
		"shell": func(script string) string {
			cmd := exec.Command("bash", "-euo", "pipefail", "-c", script)
			cmd.Dir = dir
			if options.Verbose {
				cmd.Stderr = os.Stderr
			}
			if output, err := cmd.Output(); err != nil {
				panic(err)
			} else {
				return strings.TrimRight(string(output), "\n\r")
			}
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

// buildImage builds the main Docker image for the job and returns its ID.
func buildImage(dockerfile string, dir string, buildOptions []string, readOptions ReadOptions) (string, error) {
	args := []string{"build", ".", "-f", dockerfile}
	args = append(args, buildOptions...)

	// Create a temporary file to store the image ID (@see --iidfile flag).
	tmp, err := os.CreateTemp("", "alfred-image-id-")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	// We can close the file descriptor because Docker will override the file anyway.
	_ = tmp.Close()
	defer os.Remove(tmp.Name())

	args = append(args, "--iidfile", tmp.Name())
	cmd := exec.Command("docker", args...)

	cmd.Dir = dir
	cmd.Stdout = lo.Ternary(readOptions.Verbose, os.Stdout, nil)
	cmd.Stderr = lo.Ternary(readOptions.Verbose, os.Stderr, nil)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to build image: %w", err)
	}

	imageId, err := os.ReadFile(tmp.Name())
	if err != nil {
		return "", fmt.Errorf("failed to read image id: %w", err)
	}

	return string(imageId), nil
}
