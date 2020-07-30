package transform

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/pachyderm/pachyderm/src/client/pkg/require"
	"github.com/pachyderm/pachyderm/src/client/pps"
	pfstesting "github.com/pachyderm/pachyderm/src/server/pfs/server/testing"
	"github.com/pachyderm/pachyderm/src/server/pkg/tar"
	"github.com/pachyderm/pachyderm/src/server/pkg/tarutil"
)

func TestJobSuccessV2(t *testing.T) {
	if os.Getenv("CI") == "true" {
		t.SkipNow()
	}
	pi := defaultPipelineInfo()
	pi.EnableStats = true
	require.NoError(t, withWorkerSpawnerPair(pi, func(env *testEnv) error {
		// Create temporary repo for temporary filesets.
		require.NoError(t, env.PachClient.CreateRepo("tmp"))
		ctx, etcdJobInfo := mockBasicJob(t, env, pi)
		fileName := "/file"
		fileContent := []byte("foobar")
		tarFile := tarutil.NewFile(fileName, fileContent)
		triggerJobV2(t, env, pi, []tarutil.File{tarFile})
		ctx = withTimeout(ctx, 10*time.Second)
		<-ctx.Done()
		require.Equal(t, pps.JobState_JOB_SUCCESS, etcdJobInfo.State)

		// Ensure the output commit is successful
		outputCommitID := etcdJobInfo.OutputCommit.ID
		outputCommitInfo, err := env.PachClient.InspectCommit(pi.Pipeline.Name, outputCommitID)
		require.NoError(t, err)
		require.NotNil(t, outputCommitInfo.Finished)

		branchInfo, err := env.PachClient.InspectBranch(pi.Pipeline.Name, pi.OutputBranch)
		require.NoError(t, err)
		require.NotNil(t, branchInfo)

		r, err := env.PachClient.GetTarV2(pi.Pipeline.Name, outputCommitID, fileName)
		require.NoError(t, err)
		require.NoError(t, tarutil.Iterate(r, func(file tarutil.File) error {
			hdr, err := file.Header()
			if err != nil {
				return err
			}
			require.Equal(t, fileName, hdr.Name)
			buf := &bytes.Buffer{}
			if err := file.Content(buf); err != nil {
				return err
			}
			require.True(t, bytes.Equal(fileContent, buf.Bytes()))
			return nil
		}))
		return nil
	}, pfstesting.NewPachdConfig()))
}

func triggerJobV2(t *testing.T, env *testEnv, pi *pps.PipelineInfo, files []tarutil.File) {
	commit, err := env.PachClient.StartCommit(pi.Input.Pfs.Repo, "master")
	require.NoError(t, err)
	buf := &bytes.Buffer{}
	require.NoError(t, tarutil.WithWriter(buf, func(tw *tar.Writer) error {
		for _, f := range files {
			if err := tarutil.WriteFile(tw, f); err != nil {
				return err
			}
		}
		return nil
	}))
	require.NoError(t, env.PachClient.PutTarV2(pi.Input.Pfs.Repo, commit.ID, buf))
	require.NoError(t, env.PachClient.FinishCommit(pi.Input.Pfs.Repo, commit.ID))
}