package jobfile

import (
	"os"
	"path"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var flagtests = []struct {
	file     string
	expected string
}{
	{"tests/jobfile/valid_minimalist.yaml", ""},
	{"tests/jobfile/valid_full_featured.yaml", ""},
	{"tests/jobfile/valid_with_task_slots.yaml", ""},

	{"tests/jobfile/invalid_missing_name.yaml", "name must be a valid identifier"},
	{"tests/jobfile/invalid_missing_image_dockerfile.yaml", "image.dockerfile is required"},
	{"tests/jobfile/invalid_missing_image_context.yaml", "image.context is required"},
	{"tests/jobfile/invalid_missing_services_image.yaml", "services[mysql].image is required"},
	{"tests/jobfile/invalid_missing_services_health_cmd.yaml", "services[mysql].health.cmd is required"},

	{"tests/jobfile/invalid_name.yaml", "name must be a valid identifier"},
	{"tests/jobfile/invalid_version.yaml", "unsupported version '42'"},
	{"tests/jobfile/invalid_image_dockerfile.yaml", "image.dockerfile must be an existing file on disk"},
	{"tests/jobfile/invalid_image_context.yaml", "image.context must be an existing folder on disk"},
	{"tests/jobfile/invalid_env_keys.yaml", "env[0/2] must be a valid environment variable identifier"},
	{"tests/jobfile/invalid_services_map.yaml", "yaml: unmarshal errors:\n  line 7: cannot unmarshal !!seq into map[string]jobfile.JobfileService"},
	{"tests/jobfile/invalid_services_keys.yaml", "services names must be valid identifiers"},
	{"tests/jobfile/invalid_services_env_keys.yaml", "services[mysql].env[0/2] must be a valid environment variable identifier"},
}

// --- JobfileTask UnmarshalYAML tests ---

func TestJobfileTask_PlainString(t *testing.T) {
	var task JobfileTask
	err := yaml.Unmarshal([]byte(`task-name`), &task)
	assert.NoError(t, err)
	assert.Equal(t, "task-name", task.Name)
	assert.Equal(t, uint32(1), task.Slots)
}

func TestJobfileTask_ObjectWithSlots(t *testing.T) {
	var task JobfileTask
	err := yaml.Unmarshal([]byte(`{ name: fase, slots: 8 }`), &task)
	assert.NoError(t, err)
	assert.Equal(t, "fase", task.Name)
	assert.Equal(t, uint32(8), task.Slots)
}

func TestJobfileTask_ObjectWithoutSlots(t *testing.T) {
	var task JobfileTask
	err := yaml.Unmarshal([]byte(`{ name: my-task }`), &task)
	assert.NoError(t, err)
	assert.Equal(t, "my-task", task.Name)
	assert.Equal(t, uint32(1), task.Slots, "missing slots should default to 1")
}

func TestJobfileTask_ObjectWithSlotsZero(t *testing.T) {
	var task JobfileTask
	err := yaml.Unmarshal([]byte(`{ name: my-task, slots: 0 }`), &task)
	assert.NoError(t, err)
	assert.Equal(t, "my-task", task.Name)
	assert.Equal(t, uint32(1), task.Slots, "slots=0 should default to 1")
}

func TestJobfileTask_ObjectWithSlotsOne(t *testing.T) {
	var task JobfileTask
	err := yaml.Unmarshal([]byte(`{ name: my-task, slots: 1 }`), &task)
	assert.NoError(t, err)
	assert.Equal(t, "my-task", task.Name)
	assert.Equal(t, uint32(1), task.Slots)
}

func TestJobfileTask_ListMixed(t *testing.T) {
	var tasks []JobfileTask
	err := yaml.Unmarshal([]byte("- { name: fase, slots: 8 }\n- { name: a2c }\n- a2c-plain"), &tasks)
	assert.NoError(t, err)
	require.Len(t, tasks, 3)
	assert.Equal(t, "fase", tasks[0].Name)
	assert.Equal(t, uint32(8), tasks[0].Slots)
	assert.Equal(t, "a2c", tasks[1].Name)
	assert.Equal(t, uint32(1), tasks[1].Slots)
	assert.Equal(t, "a2c-plain", tasks[2].Name)
	assert.Equal(t, uint32(1), tasks[2].Slots)
}

// --- Jobfile validation tests ---

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
