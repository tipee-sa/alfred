package job

import "fmt"

const JobfileVersion = "1"

type JobFile struct {
	Version string
	Name    string
	Image   JobFileImage
	Tasks   string
}

type JobFileImage struct {
	Dockerfile string
	Context    string
	Options    []string
}

func (jobfile JobFile) Validate() error {
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
