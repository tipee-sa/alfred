package job

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"gopkg.in/yaml.v3"
)

func Read(p string, overrides Overrides) (job *proto.Job, err error) {
	job = &proto.Job{}
	workDir := path.Join(lo.Must(os.Getwd()), path.Dir(p))

	var buf []byte
	buf, err = os.ReadFile(p)
	if err != nil {
		err = fmt.Errorf("read file: %w", err)
		return
	}

	var jobfile JobFile
	if err = yaml.Unmarshal(buf, &jobfile); err != nil {
		err = fmt.Errorf("unmarshal: %w", err)
		return
	}
	if err = jobfile.Validate(); err != nil {
		err = fmt.Errorf("validate: %w", err)
		return
	}

	// Name
	job.Name = lo.Must(lo.Coalesce(overrides.Name, jobfile.Name))

	// Tasks
	if len(overrides.Tasks) > 0 {
		job.Tasks = overrides.Tasks
	} else {
		tasks, err := shell(jobfile.Tasks, workDir)
		if err != nil {
			return job, err
		}
		job.Tasks = lo.WithoutEmpty(strings.Split(tasks, "\n"))
	}
	if len(overrides.SkipTasks) > 0 {
		job.Tasks = lo.Without(job.Tasks, overrides.SkipTasks...)
	}

	// Image
	if job.Image, err = buildImage(
		path.Join(workDir, jobfile.Image.Dockerfile),
		path.Join(workDir, jobfile.Image.Context),
		jobfile.Image.Options,
	); err != nil {
		err = fmt.Errorf("build: %w", err)
		return
	}

	return
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

func buildImage(dockerfile string, dir string, options []string) (string, error) {
	args := []string{"build", ".", "-f", dockerfile}
	args = append(args, options...)

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

	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(output)))
	}

	imageId, err := os.ReadFile(tmp.Name())
	if err != nil {
		return "", fmt.Errorf("read image id: %w", err)
	}

	return string(imageId), nil
}
