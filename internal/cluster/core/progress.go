package core

// CountCompletedSteps returns how many recorded steps completed successfully.
func CountCompletedSteps(steps []StepResult) int {
	count := 0
	for _, st := range steps {
		if st.Status == JobStatusCompleted {
			count++
		}
	}
	return count
}

// ApplyProgress fills CompletedSteps, TotalSteps, and ProgressPercent on job
// given the already-computed number of applicable steps (totalSteps). A
// completed job always reports 100%; counts are clamped to [0, totalSteps].
func ApplyProgress[Spec any](job *Job[Spec], totalSteps int) {
	job.TotalSteps = totalSteps
	job.CompletedSteps = CountCompletedSteps(job.Steps)
	if job.Status == JobStatusCompleted && totalSteps > 0 {
		job.CompletedSteps = totalSteps
	}
	if job.CompletedSteps > totalSteps {
		job.CompletedSteps = totalSteps
	}
	if job.CompletedSteps < 0 || totalSteps == 0 {
		job.ProgressPercent = 0
		return
	}
	job.ProgressPercent = job.CompletedSteps * 100 / totalSteps
}
