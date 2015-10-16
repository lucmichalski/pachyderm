package server

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/context"

	"go.pedge.io/proto/test"
	"go.pedge.io/protolog"

	"github.com/fsouza/go-dockerclient"
	"github.com/satori/go.uuid"
	"go.pachyderm.com/pachyderm/src/pfs"
	pfstesting "go.pachyderm.com/pachyderm/src/pfs/testing"
	"go.pachyderm.com/pachyderm/src/pkg/container"
	"go.pachyderm.com/pachyderm/src/pkg/require"
	"go.pachyderm.com/pachyderm/src/pps"
	"go.pachyderm.com/pachyderm/src/pps/persist"
	persistserver "go.pachyderm.com/pachyderm/src/pps/persist/server"
	"google.golang.org/grpc"
)

const (
	testNumServers = 1
)

func TestCreateAndGetPipeline(t *testing.T) {
	runTest(t, testCreateAndGetPipeline)
}

func testCreateAndGetPipeline(t *testing.T, apiClient pps.APIClient) {
	expectedPipeline := &pps.Pipeline{
		Name: "foo",
		Transform: &pps.Transform{
			Image: "ubuntu:14.04",
			Cmd: []string{
				"which",
				"bash",
			},
		},
		PipelineInput: []*pps.PipelineInput{
			&pps.PipelineInput{
				Input: &pps.PipelineInput_HostDir{
					HostDir: "/path/to/dir",
				},
			},
		},
	}
	pipeline, err := apiClient.CreatePipeline(
		context.Background(),
		&pps.CreatePipelineRequest{
			Pipeline: expectedPipeline,
		},
	)
	require.NoError(t, err)
	require.Equal(t, expectedPipeline, pipeline)
	getPipeline, err := apiClient.GetPipeline(
		context.Background(),
		&pps.GetPipelineRequest{
			PipelineName: "foo",
		},
	)
	require.NoError(t, err)
	require.Equal(t, expectedPipeline, getPipeline)
}

func TestBasicCreateAndStartJob(t *testing.T) {
	runTest(t, testBasicCreateAndStartJob)
}

func testBasicCreateAndStartJob(t *testing.T, apiClient pps.APIClient) {
	inputDir, err := ioutil.TempDir("/tmp/pachyderm-test", "")
	require.NoError(t, err)
	outputDir, err := ioutil.TempDir("/tmp/pachyderm-test", "")
	require.NoError(t, err)
	file, err := os.Create(filepath.Join(inputDir, "foo.txt"))
	require.NoError(t, err)
	_, err = file.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, file.Close())
	job := &pps.Job{
		Spec: &pps.Job_Transform{
			Transform: &pps.Transform{
				Image: "ubuntu:14.04",
				Cmd: []string{
					fmt.Sprintf("for i in /var/lib/pps/host/%s/*; do cp $i /var/lib/pps/host/%s/; done", inputDir, outputDir),
				},
			},
		},
		JobInput: []*pps.JobInput{
			&pps.JobInput{
				Input: &pps.JobInput_HostDir{
					HostDir: inputDir,
				},
			},
		},
		JobOutput: []*pps.JobOutput{
			&pps.JobOutput{
				Output: &pps.JobOutput_HostDir{
					HostDir: outputDir,
				},
			},
		},
	}
	createJob, err := apiClient.CreateJob(
		context.Background(),
		&pps.CreateJobRequest{
			Job: job,
		},
	)
	require.NoError(t, err)
	_, err = apiClient.StartJob(
		context.Background(),
		&pps.StartJobRequest{
			JobId: createJob.Id,
		},
	)
	require.NoError(t, err)
	jobStatus, err := getFinalJobStatus(apiClient, createJob.Id)
	require.NoError(t, err)
	require.Equal(t, pps.JobStatusType_JOB_STATUS_TYPE_SUCCESS, jobStatus.Type)
	data, err := ioutil.ReadFile(filepath.Join(outputDir, "foo.txt"))
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), data)
}

func getFinalJobStatus(apiClient pps.APIClient, jobID string) (*pps.JobStatus, error) {
	// TODO(pedge): not good
	ticker := time.NewTicker(time.Second)
	for i := 0; i < 20; i++ {
		<-ticker.C
		jobStatus, err := apiClient.GetJobStatus(
			context.Background(),
			&pps.GetJobStatusRequest{
				JobId: jobID,
			},
		)
		if err != nil {
			return nil, err
		}
		protolog.Printf("status at tick %d: %v\n", i, jobStatus)
		switch jobStatus.Type {
		case pps.JobStatusType_JOB_STATUS_TYPE_ERROR:
			return jobStatus, nil
		case pps.JobStatusType_JOB_STATUS_TYPE_SUCCESS:
			return jobStatus, nil
		}
	}
	return nil, fmt.Errorf("did not get final job status for %s", jobID)
}

func runTest(
	t *testing.T,
	f func(t *testing.T, apiClient pps.APIClient),
) {
	containerClient, err := getTestContainerClient()
	require.NoError(t, err)
	persistAPIServer, err := getTestRethinkAPIServer()
	require.NoError(t, err)
	persistAPIClient := persist.NewLocalAPIClient(persistAPIServer)
	pfstesting.RunTest(
		t,
		func(t *testing.T, apiClient pfs.ApiClient, internalApiClient pfs.InternalApiClient, cluster pfstesting.Cluster) {
			prototest.RunT(
				t,
				testNumServers,
				func(servers map[string]*grpc.Server) {
					for _, server := range servers {
						pps.RegisterAPIServer(server, NewAPIServer(persistAPIClient, containerClient))
					}
				},
				func(t *testing.T, clientConns map[string]*grpc.ClientConn) {
					var clientConn *grpc.ClientConn
					for _, c := range clientConns {
						clientConn = c
						break
					}
					f(
						t,
						pps.NewAPIClient(
							clientConn,
						),
					)
				},
			)
		},
	)
}

func getTestContainerClient() (container.Client, error) {
	client, err := docker.NewClientFromEnv()
	if err != nil {
		return nil, err
	}
	return container.NewDockerClient(client), nil
}

func getTestRethinkAPIServer() (persist.APIServer, error) {
	address, err := getTestRethinkAddress()
	if err != nil {
		return nil, err
	}
	databaseName := strings.Replace(uuid.NewV4().String(), "-", "", -1)
	if err := persistserver.InitDBs(address, databaseName); err != nil {
		return nil, err
	}
	return persistserver.NewRethinkAPIServer(address, databaseName)
}

func getTestRethinkAddress() (string, error) {
	rethinkAddr := os.Getenv("RETHINK_PORT_28015_TCP_ADDR")
	if rethinkAddr == "" {
		return "", errors.New("RETHINK_PORT_28015_TCP_ADDR not set")
	}
	return fmt.Sprintf("%s:28015", rethinkAddr), nil
}
