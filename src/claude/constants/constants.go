package constants

const (
	ROLEUser      = "user"
	ROLEAssistant = "assistant"
	ROLESystem    = "system"
	ROLETool      = "tool"

	ContentText       = "text"
	ContentImage      = "image"
	ContentToolUse    = "tool_use"
	ContentToolResult = "tool_result"
	ContentThinking   = "thinking"

	ToolFunction = "function"

	StopEndTurn   = "end_turn"
	StopMaxTokens = "max_tokens"
	StopToolUse   = "tool_use"
	StopError     = "error"

	EventMessageStart      = "message_start"
	EventMessageStop       = "message_stop"
	EventMessageDelta      = "message_delta"
	EventContentBlockStart = "content_block_start"
	EventContentBlockStop  = "content_block_stop"
	EventContentBlockDelta = "content_block_delta"
	EventPing              = "ping"

	DeltaText      = "text_delta"
	DeltaInputJSON = "input_json_delta"
	DeltaThinking  = "thinking_delta"
)
