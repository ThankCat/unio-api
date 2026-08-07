package workers

import "context"

// ChannelModelVerificationExecutor 提供逐模型验证任务的单次消费能力。
type ChannelModelVerificationExecutor interface {
	ExecuteNextVerification(ctx context.Context) (bool, error)
}

// ChannelModelVerificationWorker 每次 RunOnce 处理一个验证批次。
type ChannelModelVerificationWorker struct {
	executor ChannelModelVerificationExecutor
}

func NewChannelModelVerificationWorker(executor ChannelModelVerificationExecutor) *ChannelModelVerificationWorker {
	if executor == nil {
		panic("workers: channel model verification executor is required")
	}
	return &ChannelModelVerificationWorker{executor: executor}
}

func (w *ChannelModelVerificationWorker) Name() string { return "channel_model_verification" }

func (w *ChannelModelVerificationWorker) RunOnce(ctx context.Context) (bool, error) {
	return w.executor.ExecuteNextVerification(ctx)
}

var _ Unit = (*ChannelModelVerificationWorker)(nil)
