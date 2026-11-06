package jobfile

import (
	"os"
	"path"
	"path/filepath"
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
	{"tests/jobfile/invalid_unknown_key.yaml", "yaml: unmarshal errors:\n  line 11: field wait not found in type jobfile.JobfileServiceHealth"},
	{"tests/jobfile/invalid_task_unknown_key.yaml", "line 8: field slot not found in type jobfile.JobfileTask"},
}

// --- Unknown keys and YAML merge keys ---

// A merge key ("<<") is not an unknown key: what it merges is checked like the rest, in task
// objects (checkTaskKeys) as in the jobfile's structs (the decoder's KnownFields).
func TestParseJobfile_MergeKeys(t *testing.T) {
	const head = "version: \"1\"\nname: test\nsteps: [{dockerfile: a.dockerfile, context: .}]\n"
	tests := []struct {
		name     string
		yaml     string
		expected string
	}{
		{"task merging an alias", "tasks: [&base {name: one, slots: 4}, {<<: *base, name: two}]", ""},
		{"task merging a list of aliases", "tasks: [&a {name: one}, &b {name: two, slots: 3}, {<<: [*a, *b], name: three}]", ""},
		{"task merging an unknown key inline", "tasks: [{<<: {slot: 4}, name: two}]", "line 4: field slot not found in type jobfile.JobfileTask"},
		{"task merging an unknown key from a list", "tasks: [&a {name: one}, {<<: [*a, {slot: 4}], name: two}]", "line 4: field slot not found in type jobfile.JobfileTask"},
		{"task with a quoted \"<<\" key", "tasks: [{\"<<\": x, name: two}]", "line 4: field << not found in type jobfile.JobfileTask"},
		{"service merging an alias", "services:\n  a: &svc {image: redis, health: {cmd: [true]}}\n  b: {<<: *svc, command: [x]}\ntasks: [one]", ""},
		{"service merging an unknown key", "services:\n  a: {<<: {image: redis, imag: x}, health: {cmd: [true]}}\ntasks: [one]", "yaml: unmarshal errors:\n  line 5: field imag not found in type jobfile.JobfileService"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseJobfile([]byte(head + tt.yaml))
			if tt.expected == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.expected)
			}
		})
	}

	jobfile, err := parseJobfile([]byte(head + "tasks: [&base {name: one, slots: 4}, {<<: *base, name: two}]"))
	require.NoError(t, err)
	assert.Equal(t, []JobfileTask{{Name: "one", Slots: 4}, {Name: "two", Slots: 4}}, jobfile.Tasks)
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
	for _, tt := range flagtests {
		t.Run(tt.file, func(t *testing.T) {
			buf := lo.Must(os.ReadFile(tt.file))

			jobfile, err := parseJobfile(buf)
			if err != nil {
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

func TestJobfileDir(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)

	assert.Equal(t, "/srv/jobs", jobfileDir("/srv/jobs/job.yaml"), "absolute path, not joined to the working directory")
	assert.Equal(t, filepath.Join(wd, "hack"), jobfileDir("hack/job.yaml"))
	assert.Equal(t, wd, jobfileDir("job.yaml"))
	assert.Equal(t, filepath.Dir(wd), jobfileDir("../job.yaml"))
}
