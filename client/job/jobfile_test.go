package job

import (
	"os"
	"path"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

var flagtests = []struct {
	file     string
	expected string
}{
	{"tests/jobfile/valid_minimalist.yaml", ""},
	{"tests/jobfile/valid_full_featured.yaml", ""},

	{"tests/jobfile/invalid_missing_name.yaml", "name is required"},
	{"tests/jobfile/invalid_missing_image_dockerfile.yaml", "image.dockerfile is required"},
	{"tests/jobfile/invalid_missing_image_context.yaml", "image.context is required"},
	{"tests/jobfile/invalid_missing_services_image.yaml", "services[mysql].image is required"},
	{"tests/jobfile/invalid_missing_services_health_cmd.yaml", "services[mysql].health.cmd is required"},

	{"tests/jobfile/invalid_version.yaml", "unsupported version '42'"},
	{"tests/jobfile/invalid_image_dockerfile.yaml", "image.dockerfile must be an existing file on disk"},
	{"tests/jobfile/invalid_image_context.yaml", "image.context must be an existing folder on disk"},
	{"tests/jobfile/invalid_env_keys.yaml", "env[0/2] must be a valid environment variable identifier"},
	{"tests/jobfile/invalid_services_map.yaml", "yaml: unmarshal errors:\n  line 7: cannot unmarshal !!seq into map[string]job.JobfileService"},
	{"tests/jobfile/invalid_services_keys.yaml", "services names must be valid identifiers"},
	{"tests/jobfile/invalid_services_env_keys.yaml", "services[mysql].env[0/2] must be a valid environment variable identifier"},
}

func TestJobValidate(t *testing.T) {
	var buf []byte
	var err error

	for _, tt := range flagtests {
		t.Run(tt.file, func(t *testing.T) {
			buf = lo.Must(os.ReadFile(tt.file))

			var jobfile Jobfile
			if err = yaml.Unmarshal(buf, &jobfile); err != nil {
				assert.Equal(t, tt.expected, err.Error())
				return
			}
			jobfile.path = path.Dir(tt.file)
			if err = jobfile.Validate(); err != nil {
				assert.Equal(t, tt.expected, err.Error())
				return
			}

			assert.Equal(t, "", tt.expected)
		})
	}
}
