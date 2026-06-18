package sqlc

import "github.com/jackc/pgx/v5/pgtype"

// RequestAttemptFromCreateRow 将 CreateRequestAttempt 的显式 RETURNING 行映射为 RequestAttempt。
// sqlc 对列清单 RETURNING 生成独立 Row 类型，字段顺序与 models.RequestAttempt 不同，不能直接强转。
func RequestAttemptFromCreateRow(row CreateRequestAttemptRow) RequestAttempt {
	return requestAttemptFromExplicitReturning(
		row.ID, row.RequestRecordID, row.AttemptIndex, row.ProviderID, row.ChannelID,
		row.AdapterKey, row.UpstreamModel, row.UpstreamProtocol,
		row.UpstreamResponseID, row.UpstreamResponseModel, row.UpstreamFinishReason, row.FinishClass,
		row.Status, row.UpstreamStatusCode, row.UpstreamRequestID,
		row.ErrorCode, row.ErrorMessage, row.InternalErrorDetail, row.ResponseStartedAt,
		row.FinalUsageReceived, row.UsageMappingVersion, row.RequiredCapabilities, row.UsedCapabilities,
		row.StartedAt, row.CompletedAt, row.CreatedAt,
	)
}

func requestAttemptFromExplicitReturning(
	id int64,
	requestRecordID int64,
	attemptIndex int32,
	providerID int64,
	channelID int64,
	adapterKey string,
	upstreamModel string,
	upstreamProtocol string,
	upstreamResponseID pgtype.Text,
	upstreamResponseModel pgtype.Text,
	upstreamFinishReason pgtype.Text,
	finishClass pgtype.Text,
	status string,
	upstreamStatusCode pgtype.Int4,
	upstreamRequestID pgtype.Text,
	errorCode pgtype.Text,
	errorMessage pgtype.Text,
	internalErrorDetail pgtype.Text,
	responseStartedAt pgtype.Timestamptz,
	finalUsageReceived bool,
	usageMappingVersion pgtype.Text,
	requiredCapabilities []string,
	usedCapabilities []string,
	startedAt pgtype.Timestamptz,
	completedAt pgtype.Timestamptz,
	createdAt pgtype.Timestamptz,
) RequestAttempt {
	return RequestAttempt{
		ID:                    id,
		RequestRecordID:       requestRecordID,
		AttemptIndex:          attemptIndex,
		ProviderID:            providerID,
		ChannelID:             channelID,
		AdapterKey:            adapterKey,
		UpstreamModel:         upstreamModel,
		UpstreamProtocol:      upstreamProtocol,
		UpstreamResponseID:    upstreamResponseID,
		UpstreamResponseModel: upstreamResponseModel,
		UpstreamFinishReason:  upstreamFinishReason,
		FinishClass:           finishClass,
		Status:                status,
		UpstreamStatusCode:    upstreamStatusCode,
		UpstreamRequestID:     upstreamRequestID,
		ErrorCode:             errorCode,
		ErrorMessage:          errorMessage,
		InternalErrorDetail:   internalErrorDetail,
		ResponseStartedAt:     responseStartedAt,
		FinalUsageReceived:    finalUsageReceived,
		UsageMappingVersion:   usageMappingVersion,
		RequiredCapabilities:  requiredCapabilities,
		StartedAt:             startedAt,
		CompletedAt:           completedAt,
		CreatedAt:             createdAt,
		UsedCapabilities:      usedCapabilities,
	}
}
