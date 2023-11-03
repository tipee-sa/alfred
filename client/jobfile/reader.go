package jobfile

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/gammadia/alfred/proto"
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

func Read(file string, options ReadOptions) (job *proto.Job, err error) {
	job = &proto.Job{}
	workDir := path.Join(lo.Must(os.Getwd()), path.Dir(file))

	var buf []byte
	if buf, err = os.ReadFile(file); err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	source, err := evaluateTemplate(string(buf), workDir, options)
	if err != nil {
		return nil, fmt.Errorf("evaluate template: %w", err)
	}

	var jobfile Jobfile
	if err = yaml.Unmarshal([]byte(source), &jobfile); err != nil {
		return nil, UnmarshalError{fmt.Errorf("unmarshal: %w", err), source}
	}
	jobfile.path = workDir
	if err = jobfile.Validate(); err != nil {
		return nil, UnmarshalError{fmt.Errorf("validate: %w", err), source}
	}

	// Name
	job.Name = jobfile.Name

	// Tasks
	job.Tasks = jobfile.Tasks

	// Image
	if job.Image, err = buildImage(
		path.Join(workDir, jobfile.Image.Dockerfile),
		path.Join(workDir, jobfile.Image.Context),
		jobfile.Image.Options,
		options,
	); err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}

	// Env
	if len(jobfile.Env) > 0 {
		job.Env = lo.MapToSlice(jobfile.Env, func(key string, value string) *proto.Job_Env {
			return &proto.Job_Env{
				Key:   key,
				Value: value,
			}
		})
	}

	// Services
	for name, service := range jobfile.Services {
		jobService := &proto.Job_Service{
			Name:  name,
			Image: service.Image,
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
	tmpl, err := template.New("jobfile").Funcs(template.FuncMap{
		"base64": func(s string) string {
			return base64.StdEncoding.EncodeToString([]byte(s))
		},
		"env": func(key string) string {
			return os.Getenv(key)
		},
		"json": func(v any) (string, error) {
			buf, err := json.Marshal(v)
			return string(buf), err
		},
		"lines": func(s string) []string {
			return strings.Split(s, "\n")
		},
		"shell": func(script string) (string, error) {
			return shell(script, dir)
		},
		"split": func(sep string, s string) []string {
			return strings.Split(s, sep)
		},
	}).Parse(source)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	data := TemplateData{
		Env:    lo.SliceToMap(os.Environ(), func(env string) (key, val string) { key, val, _ = strings.Cut(env, "="); return }),
		Args:   options.Args,
		Params: options.Params,
	}

	var output strings.Builder
	if err := tmpl.Execute(&output, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return output.String(), nil
}

func shell(script string, dir string) (string, error) {
	var shell, arg string
	if strings.HasPrefix(script, "#!") {
		shell, script, _ = strings.Cut(script, "\n")
		shell, arg, _ = strings.Cut(strings.TrimPrefix(shell, "#!"), " ")
	} else {
		shell = lo.Must(lo.Coalesce(os.Getenv("SHELL"), "sh"))
	}

	cmd := exec.Command(shell, lo.Ternary(arg != "", []string{arg}, []string{})...)
	cmd.Stdin = strings.NewReader(script)
	cmd.Stderr = os.Stderr
	cmd.Dir = dir

	output, err := cmd.Output()
	return string(output), err
}

// buildImage builds the main Docker image for the job and returns its ID.
func buildImage(dockerfile string, dir string, buildOptions []string, readOptions ReadOptions) (string, error) {
	args := []string{"build", ".", "-f", dockerfile}
	args = append(args, buildOptions...)

	// Create a temporary file to store the image ID (@see --iidfile flag).
	tmp, err := os.CreateTemp("", "alfred-image-id-")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}

	// We can close the file descriptor because Docker will override the file anyway.
	_ = tmp.Close()
	defer os.Remove(tmp.Name())

	args = append(args, "--iidfile", tmp.Name())
	cmd := exec.Command("docker", args...)

	cmd.Dir = dir

	if readOptions.Verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stdout = nil
		cmd.Stderr = nil
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w", err)
	}

	imageId, err := os.ReadFile(tmp.Name())
	if err != nil {
		return "", fmt.Errorf("read image id: %w", err)
	}

	return string(imageId), nil
}
