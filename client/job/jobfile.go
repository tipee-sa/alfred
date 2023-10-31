package job

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"time"
)

const JobfileVersion = "1"

type Jobfile struct {
	Path     string
	Version  string
	Name     string
	Image    JobfileImage
	Services map[string]JobfileService
	Tasks    string
}

type JobfileImage struct {
	Dockerfile string
	Context    string
	Options    []string
}

type JobfileService struct {
	Image  string
	Env    map[string]string
	Health JobfileServiceHealth
}

type JobfileServiceHealth struct {
	Cmd      []string
	Timeout  string
	Interval string
	Retries  string
}

func (jobfile Jobfile) Validate() error {
	if jobfile.Version != JobfileVersion {
		return fmt.Errorf("unsupported version '%s'", jobfile.Version)
	}

	if jobfile.Name == "" {
		return fmt.Errorf("name is required")
	}

	if jobfile.Image.Dockerfile == "" {
		return fmt.Errorf("image.dockerfile is required")
	}
	if _, err := os.Stat(path.Join(jobfile.Path, jobfile.Image.Dockerfile)); os.IsNotExist(err) {
		return fmt.Errorf("image.dockerfile must be an existing file on disk")
	}

	if jobfile.Image.Context == "" {
		return fmt.Errorf("image.context is required")
	}
	if _, err := os.Stat(path.Join(jobfile.Path, jobfile.Image.Context)); os.IsNotExist(err) {
		return fmt.Errorf("image.context must be an existing folder on disk")
	}

	serviceNameRegex := regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]+$`)
	serviceEnvKeyRegex := regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)

	for name, service := range jobfile.Services {
		if !serviceNameRegex.MatchString(name) {
			return fmt.Errorf("services names must be valid identifiers")
		}

		if service.Image == "" {
			return fmt.Errorf("services[%s].image is required", name)
		}

		for key, _ := range service.Env {
			if !serviceEnvKeyRegex.MatchString(key) {
				return fmt.Errorf("services[%s].env[%s] must be a valid environment variable identifier", name, key)
			}
		}

		// If none of the health fields are specified, skip validation
		if service.Health.Cmd == nil && service.Health.Timeout == "" && service.Health.Interval == "" && service.Health.Retries == "" {
			continue
		}

		if len(service.Health.Cmd) < 1 {
			return fmt.Errorf("services[%s].health.cmd is required", name)
		}

		if service.Health.Interval != "" {
			if _, err := time.ParseDuration(service.Health.Interval); err != nil {
				return fmt.Errorf("services[%s].health.interval is not a valid duration: %w", name, err)
			}
		}

		if service.Health.Timeout != "" {
			if _, err := time.ParseDuration(service.Health.Timeout); err != nil {
				return fmt.Errorf("services[%s].health.timeout is not a valid duration: %w", name, err)
			}
		}

		if service.Health.Retries != "" {
			if _, err := strconv.ParseInt(service.Health.Retries, 10, 64); err != nil {
				return fmt.Errorf("services[%s].health.retries is not a valid numeric: %w", name, err)
			}
		}
	}

	return nil
}
