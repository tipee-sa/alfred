package job

import (
	"fmt"
)

const JobfileVersion = "1"

type Jobfile struct {
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
	Cmd []string
}

func (jobfile Jobfile) Validate() error {
	if jobfile.Version != JobfileVersion {
		return fmt.Errorf("unsupported version '%s'", jobfile.Version)
	}

	if jobfile.Name == "" {
		return fmt.Errorf("job name is required")
	}

	if jobfile.Image.Dockerfile == "" {
		return fmt.Errorf("image.dockerfile is required")
	}

	if jobfile.Image.Context == "" {
		return fmt.Errorf("image.context is required")
	}

	return nil
}
