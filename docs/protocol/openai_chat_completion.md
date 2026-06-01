https://developers.openai.com/api/reference/go/resources/chat/subresources/completions/methods/create
## Create chat completion

`client.Chat.Completions.New(ctx, body) (*ChatCompletion, error)`

**post** `/chat/completions`

**Starting a new project?** We recommend trying [Responses](https://platform.openai.com/docs/api-reference/responses)
to take advantage of the latest OpenAI platform features. Compare
[Chat Completions with Responses](https://platform.openai.com/docs/guides/responses-vs-chat-completions?api-mode=responses).

---

Creates a model response for the given chat conversation. Learn more in the
[text generation](https://platform.openai.com/docs/guides/text-generation), [vision](https://platform.openai.com/docs/guides/vision),
and [audio](https://platform.openai.com/docs/guides/audio) guides.

Parameter support can differ depending on the model used to generate the
response, particularly for newer reasoning models. Parameters that are only
supported for reasoning models are noted below. For the current state of
unsupported parameters in reasoning models,
[refer to the reasoning guide](https://platform.openai.com/docs/guides/reasoning).

Returns a chat completion object, or a streamed sequence of chat completion
chunk objects if the request is streamed.

### Parameters

- `body ChatCompletionNewParams`

    - `Messages param.Field[[]ChatCompletionMessageParamUnionResp]`

      A list of messages comprising the conversation so far. Depending on the
      [model](https://platform.openai.com/docs/models) you use, different message types (modalities) are
      supported, like [text](https://platform.openai.com/docs/guides/text-generation),
      [images](https://platform.openai.com/docs/guides/vision), and [audio](https://platform.openai.com/docs/guides/audio).

        - `type ChatCompletionDeveloperMessageParamResp struct{…}`

          Developer-provided instructions that the model should follow, regardless of
          messages sent by the user. With o1 models and newer, `developer` messages
          replace the previous `system` messages.

            - `Content ChatCompletionDeveloperMessageParamContentUnionResp`

              The contents of the developer message.

                - `string`

                - `[]ChatCompletionContentPartText`

                    - `Text string`

                      The text content.

                    - `Type Text`

                      The type of the content part.

                        - `const TextText Text = "text"`

            - `Role Developer`

              The role of the messages author, in this case `developer`.

                - `const DeveloperDeveloper Developer = "developer"`

            - `Name string`

              An optional name for the participant. Provides the model information to differentiate between participants of the same role.

        - `type ChatCompletionSystemMessageParamResp struct{…}`

          Developer-provided instructions that the model should follow, regardless of
          messages sent by the user. With o1 models and newer, use `developer` messages
          for this purpose instead.

            - `Content ChatCompletionSystemMessageParamContentUnionResp`

              The contents of the system message.

                - `string`

                - `[]ChatCompletionContentPartText`

                    - `Text string`

                      The text content.

                    - `Type Text`

                      The type of the content part.

            - `Role System`

              The role of the messages author, in this case `system`.

                - `const SystemSystem System = "system"`

            - `Name string`

              An optional name for the participant. Provides the model information to differentiate between participants of the same role.

        - `type ChatCompletionUserMessageParamResp struct{…}`

          Messages sent by an end user, containing prompts or additional context
          information.

            - `Content ChatCompletionUserMessageParamContentUnionResp`

              The contents of the user message.

                - `string`

                - `[]ChatCompletionContentPartUnion`

                    - `type ChatCompletionContentPartText struct{…}`

                      Learn about [text inputs](https://platform.openai.com/docs/guides/text-generation).

                        - `Text string`

                          The text content.

                        - `Type Text`

                          The type of the content part.

                    - `type ChatCompletionContentPartImage struct{…}`

                      Learn about [image inputs](https://platform.openai.com/docs/guides/vision).

                        - `ImageURL ChatCompletionContentPartImageImageURL`

                            - `URL string`

                              Either a URL of the image or the base64 encoded image data.

                            - `Detail string`

                              Specifies the detail level of the image. Learn more in the [Vision guide](https://platform.openai.com/docs/guides/vision#low-or-high-fidelity-image-understanding).

                                - `const ChatCompletionContentPartImageImageURLDetailAuto ChatCompletionContentPartImageImageURLDetail = "auto"`

                                - `const ChatCompletionContentPartImageImageURLDetailLow ChatCompletionContentPartImageImageURLDetail = "low"`

                                - `const ChatCompletionContentPartImageImageURLDetailHigh ChatCompletionContentPartImageImageURLDetail = "high"`

                        - `Type ImageURL`

                          The type of the content part.

                            - `const ImageURLImageURL ImageURL = "image_url"`

                    - `type ChatCompletionContentPartInputAudio struct{…}`

                      Learn about [audio inputs](https://platform.openai.com/docs/guides/audio).

                        - `InputAudio ChatCompletionContentPartInputAudioInputAudio`

                            - `Data string`

                              Base64 encoded audio data.

                            - `Format string`

                              The format of the encoded audio data. Currently supports "wav" and "mp3".

                                - `const ChatCompletionContentPartInputAudioInputAudioFormatWAV ChatCompletionContentPartInputAudioInputAudioFormat = "wav"`

                                - `const ChatCompletionContentPartInputAudioInputAudioFormatMP3 ChatCompletionContentPartInputAudioInputAudioFormat = "mp3"`

                        - `Type InputAudio`

                          The type of the content part. Always `input_audio`.

                            - `const InputAudioInputAudio InputAudio = "input_audio"`

                    - `ChatCompletionContentPartFile`

                        - `File ChatCompletionContentPartFileFile`

                            - `FileData string`

                              The base64 encoded file data, used when passing the file to the model
                              as a string.

                            - `FileID string`

                              The ID of an uploaded file to use as input.

                            - `Filename string`

                              The name of the file, used when passing the file to the model as a
                              string.

                        - `Type File`

                          The type of the content part. Always `file`.

                            - `const FileFile File = "file"`

            - `Role User`

              The role of the messages author, in this case `user`.

                - `const UserUser User = "user"`

            - `Name string`

              An optional name for the participant. Provides the model information to differentiate between participants of the same role.

        - `type ChatCompletionAssistantMessageParamResp struct{…}`

          Messages sent by the model in response to user messages.

            - `Role Assistant`

              The role of the messages author, in this case `assistant`.

                - `const AssistantAssistant Assistant = "assistant"`

            - `Audio ChatCompletionAssistantMessageParamAudioResp`

              Data about a previous audio response from the model.
              [Learn more](https://platform.openai.com/docs/guides/audio).

                - `ID string`

                  Unique identifier for a previous audio response from the model.

            - `Content ChatCompletionAssistantMessageParamContentUnionResp`

              The contents of the assistant message. Required unless `tool_calls` or `function_call` is specified.

                - `string`

                - `[]ChatCompletionAssistantMessageParamContentArrayOfContentPartUnionResp`

                    - `type ChatCompletionContentPartText struct{…}`

                      Learn about [text inputs](https://platform.openai.com/docs/guides/text-generation).

                    - `type ChatCompletionContentPartRefusal struct{…}`

                        - `Refusal string`

                          The refusal message generated by the model.

                        - `Type Refusal`

                          The type of the content part.

                            - `const RefusalRefusal Refusal = "refusal"`

            - `FunctionCall ChatCompletionAssistantMessageParamFunctionCallResp`

              Deprecated and replaced by `tool_calls`. The name and arguments of a function that should be called, as generated by the model.

                - `Arguments string`

                  The arguments to call the function with, as generated by the model in JSON format. Note that the model does not always generate valid JSON, and may hallucinate parameters not defined by your function schema. Validate the arguments in your code before calling your function.

                - `Name string`

                  The name of the function to call.

            - `Name string`

              An optional name for the participant. Provides the model information to differentiate between participants of the same role.

            - `Refusal string`

              The refusal message by the assistant.

            - `ToolCalls []ChatCompletionMessageToolCallUnion`

              The tool calls generated by the model, such as function calls.

                - `type ChatCompletionMessageFunctionToolCall struct{…}`

                  A call to a function tool created by the model.

                    - `ID string`

                      The ID of the tool call.

                    - `Function ChatCompletionMessageFunctionToolCallFunction`

                      The function that the model called.

                        - `Arguments string`

                          The arguments to call the function with, as generated by the model in JSON format. Note that the model does not always generate valid JSON, and may hallucinate parameters not defined by your function schema. Validate the arguments in your code before calling your function.

                        - `Name string`

                          The name of the function to call.

                    - `Type Function`

                      The type of the tool. Currently, only `function` is supported.

                        - `const FunctionFunction Function = "function"`

                - `type ChatCompletionMessageCustomToolCall struct{…}`

                  A call to a custom tool created by the model.

                    - `ID string`

                      The ID of the tool call.

                    - `Custom ChatCompletionMessageCustomToolCallCustom`

                      The custom tool that the model called.

                        - `Input string`

                          The input for the custom tool call generated by the model.

                        - `Name string`

                          The name of the custom tool to call.

                    - `Type Custom`

                      The type of the tool. Always `custom`.

                        - `const CustomCustom Custom = "custom"`

        - `type ChatCompletionToolMessageParamResp struct{…}`

            - `Content ChatCompletionToolMessageParamContentUnionResp`

              The contents of the tool message.

                - `string`

                - `[]ChatCompletionContentPartText`

                    - `Text string`

                      The text content.

                    - `Type Text`

                      The type of the content part.

            - `Role Tool`

              The role of the messages author, in this case `tool`.

                - `const ToolTool Tool = "tool"`

            - `ToolCallID string`

              Tool call that this message is responding to.

        - `type ChatCompletionFunctionMessageParamResp struct{…}`

            - `Content string`

              The contents of the function message.

            - `Name string`

              The name of the function to call.

            - `Role Function`

              The role of the messages author, in this case `function`.

                - `const FunctionFunction Function = "function"`

    - `Model param.Field[ChatModel]`

      Model ID used to generate the response, like `gpt-4o` or `o3`. OpenAI
      offers a wide range of models with different capabilities, performance
      characteristics, and price points. Refer to the [model guide](https://platform.openai.com/docs/models)
      to browse and compare available models.

        - `string`

        - `type ChatModel string`

            - `const ChatModelGPT5_4 ChatModel = "gpt-5.4"`

            - `const ChatModelGPT5_4Mini ChatModel = "gpt-5.4-mini"`

            - `const ChatModelGPT5_4Nano ChatModel = "gpt-5.4-nano"`

            - `const ChatModelGPT5_4Mini2026_03_17 ChatModel = "gpt-5.4-mini-2026-03-17"`

            - `const ChatModelGPT5_4Nano2026_03_17 ChatModel = "gpt-5.4-nano-2026-03-17"`

            - `const ChatModelGPT5_3ChatLatest ChatModel = "gpt-5.3-chat-latest"`

            - `const ChatModelGPT5_2 ChatModel = "gpt-5.2"`

            - `const ChatModelGPT5_2_2025_12_11 ChatModel = "gpt-5.2-2025-12-11"`

            - `const ChatModelGPT5_2ChatLatest ChatModel = "gpt-5.2-chat-latest"`

            - `const ChatModelGPT5_2Pro ChatModel = "gpt-5.2-pro"`

            - `const ChatModelGPT5_2Pro2025_12_11 ChatModel = "gpt-5.2-pro-2025-12-11"`

            - `const ChatModelGPT5_1 ChatModel = "gpt-5.1"`

            - `const ChatModelGPT5_1_2025_11_13 ChatModel = "gpt-5.1-2025-11-13"`

            - `const ChatModelGPT5_1Codex ChatModel = "gpt-5.1-codex"`

            - `const ChatModelGPT5_1Mini ChatModel = "gpt-5.1-mini"`

            - `const ChatModelGPT5_1ChatLatest ChatModel = "gpt-5.1-chat-latest"`

            - `const ChatModelGPT5 ChatModel = "gpt-5"`

            - `const ChatModelGPT5Mini ChatModel = "gpt-5-mini"`

            - `const ChatModelGPT5Nano ChatModel = "gpt-5-nano"`

            - `const ChatModelGPT5_2025_08_07 ChatModel = "gpt-5-2025-08-07"`

            - `const ChatModelGPT5Mini2025_08_07 ChatModel = "gpt-5-mini-2025-08-07"`

            - `const ChatModelGPT5Nano2025_08_07 ChatModel = "gpt-5-nano-2025-08-07"`

            - `const ChatModelGPT5ChatLatest ChatModel = "gpt-5-chat-latest"`

            - `const ChatModelGPT4_1 ChatModel = "gpt-4.1"`

            - `const ChatModelGPT4_1Mini ChatModel = "gpt-4.1-mini"`

            - `const ChatModelGPT4_1Nano ChatModel = "gpt-4.1-nano"`

            - `const ChatModelGPT4_1_2025_04_14 ChatModel = "gpt-4.1-2025-04-14"`

            - `const ChatModelGPT4_1Mini2025_04_14 ChatModel = "gpt-4.1-mini-2025-04-14"`

            - `const ChatModelGPT4_1Nano2025_04_14 ChatModel = "gpt-4.1-nano-2025-04-14"`

            - `const ChatModelO4Mini ChatModel = "o4-mini"`

            - `const ChatModelO4Mini2025_04_16 ChatModel = "o4-mini-2025-04-16"`

            - `const ChatModelO3 ChatModel = "o3"`

            - `const ChatModelO3_2025_04_16 ChatModel = "o3-2025-04-16"`

            - `const ChatModelO3Mini ChatModel = "o3-mini"`

            - `const ChatModelO3Mini2025_01_31 ChatModel = "o3-mini-2025-01-31"`

            - `const ChatModelO1 ChatModel = "o1"`

            - `const ChatModelO1_2024_12_17 ChatModel = "o1-2024-12-17"`

            - `const ChatModelO1Preview ChatModel = "o1-preview"`

            - `const ChatModelO1Preview2024_09_12 ChatModel = "o1-preview-2024-09-12"`

            - `const ChatModelO1Mini ChatModel = "o1-mini"`

            - `const ChatModelO1Mini2024_09_12 ChatModel = "o1-mini-2024-09-12"`

            - `const ChatModelGPT4o ChatModel = "gpt-4o"`

            - `const ChatModelGPT4o2024_11_20 ChatModel = "gpt-4o-2024-11-20"`

            - `const ChatModelGPT4o2024_08_06 ChatModel = "gpt-4o-2024-08-06"`

            - `const ChatModelGPT4o2024_05_13 ChatModel = "gpt-4o-2024-05-13"`

            - `const ChatModelGPT4oAudioPreview ChatModel = "gpt-4o-audio-preview"`

            - `const ChatModelGPT4oAudioPreview2024_10_01 ChatModel = "gpt-4o-audio-preview-2024-10-01"`

            - `const ChatModelGPT4oAudioPreview2024_12_17 ChatModel = "gpt-4o-audio-preview-2024-12-17"`

            - `const ChatModelGPT4oAudioPreview2025_06_03 ChatModel = "gpt-4o-audio-preview-2025-06-03"`

            - `const ChatModelGPT4oMiniAudioPreview ChatModel = "gpt-4o-mini-audio-preview"`

            - `const ChatModelGPT4oMiniAudioPreview2024_12_17 ChatModel = "gpt-4o-mini-audio-preview-2024-12-17"`

            - `const ChatModelGPT4oSearchPreview ChatModel = "gpt-4o-search-preview"`

            - `const ChatModelGPT4oMiniSearchPreview ChatModel = "gpt-4o-mini-search-preview"`

            - `const ChatModelGPT4oSearchPreview2025_03_11 ChatModel = "gpt-4o-search-preview-2025-03-11"`

            - `const ChatModelGPT4oMiniSearchPreview2025_03_11 ChatModel = "gpt-4o-mini-search-preview-2025-03-11"`

            - `const ChatModelChatgpt4oLatest ChatModel = "chatgpt-4o-latest"`

            - `const ChatModelCodexMiniLatest ChatModel = "codex-mini-latest"`

            - `const ChatModelGPT4oMini ChatModel = "gpt-4o-mini"`

            - `const ChatModelGPT4oMini2024_07_18 ChatModel = "gpt-4o-mini-2024-07-18"`

            - `const ChatModelGPT4Turbo ChatModel = "gpt-4-turbo"`

            - `const ChatModelGPT4Turbo2024_04_09 ChatModel = "gpt-4-turbo-2024-04-09"`

            - `const ChatModelGPT4_0125Preview ChatModel = "gpt-4-0125-preview"`

            - `const ChatModelGPT4TurboPreview ChatModel = "gpt-4-turbo-preview"`

            - `const ChatModelGPT4_1106Preview ChatModel = "gpt-4-1106-preview"`

            - `const ChatModelGPT4VisionPreview ChatModel = "gpt-4-vision-preview"`

            - `const ChatModelGPT4 ChatModel = "gpt-4"`

            - `const ChatModelGPT4_0314 ChatModel = "gpt-4-0314"`

            - `const ChatModelGPT4_0613 ChatModel = "gpt-4-0613"`

            - `const ChatModelGPT4_32k ChatModel = "gpt-4-32k"`

            - `const ChatModelGPT4_32k0314 ChatModel = "gpt-4-32k-0314"`

            - `const ChatModelGPT4_32k0613 ChatModel = "gpt-4-32k-0613"`

            - `const ChatModelGPT3_5Turbo ChatModel = "gpt-3.5-turbo"`

            - `const ChatModelGPT3_5Turbo16k ChatModel = "gpt-3.5-turbo-16k"`

            - `const ChatModelGPT3_5Turbo0301 ChatModel = "gpt-3.5-turbo-0301"`

            - `const ChatModelGPT3_5Turbo0613 ChatModel = "gpt-3.5-turbo-0613"`

            - `const ChatModelGPT3_5Turbo1106 ChatModel = "gpt-3.5-turbo-1106"`

            - `const ChatModelGPT3_5Turbo0125 ChatModel = "gpt-3.5-turbo-0125"`

            - `const ChatModelGPT3_5Turbo16k0613 ChatModel = "gpt-3.5-turbo-16k-0613"`

    - `Audio param.Field[ChatCompletionAudioParamResp]`

      Parameters for audio output. Required when audio output is requested with
      `modalities: ["audio"]`. [Learn more](https://platform.openai.com/docs/guides/audio).

    - `FrequencyPenalty param.Field[float64]`

      Number between -2.0 and 2.0. Positive values penalize new tokens based on
      their existing frequency in the text so far, decreasing the model's
      likelihood to repeat the same line verbatim.

    - `FunctionCall param.Field[ChatCompletionNewParamsFunctionCallUnion]`

      Deprecated in favor of `tool_choice`.

      Controls which (if any) function is called by the model.

      `none` means the model will not call a function and instead generates a
      message.

      `auto` means the model can pick between generating a message or calling a
      function.

      Specifying a particular function via `{"name": "my_function"}` forces the
      model to call that function.

      `none` is the default when no functions are present. `auto` is the default
      if functions are present.

        - `string`

            - `const ChatCompletionNewParamsFunctionCallFunctionCallModeNone ChatCompletionNewParamsFunctionCallFunctionCallMode = "none"`

            - `const ChatCompletionNewParamsFunctionCallFunctionCallModeAuto ChatCompletionNewParamsFunctionCallFunctionCallMode = "auto"`

        - `type ChatCompletionFunctionCallOption struct{…}`

          Specifying a particular function via `{"name": "my_function"}` forces the model to call that function.

            - `Name string`

              The name of the function to call.

    - `Functions param.Field[[]ChatCompletionNewParamsFunction]`

      Deprecated in favor of `tools`.

      A list of functions the model may generate JSON inputs for.

        - `Name string`

          The name of the function to be called. Must be a-z, A-Z, 0-9, or contain underscores and dashes, with a maximum length of 64.

        - `Description string`

          A description of what the function does, used by the model to choose when and how to call the function.

        - `Parameters FunctionParameters`

          The parameters the functions accepts, described as a JSON Schema object. See the [guide](https://platform.openai.com/docs/guides/function-calling) for examples, and the [JSON Schema reference](https://json-schema.org/understanding-json-schema/) for documentation about the format.

          Omitting `parameters` defines a function with an empty parameter list.

    - `LogitBias param.Field[map[string, int64]]`

      Modify the likelihood of specified tokens appearing in the completion.

      Accepts a JSON object that maps tokens (specified by their token ID in the
      tokenizer) to an associated bias value from -100 to 100. Mathematically,
      the bias is added to the logits generated by the model prior to sampling.
      The exact effect will vary per model, but values between -1 and 1 should
      decrease or increase likelihood of selection; values like -100 or 100
      should result in a ban or exclusive selection of the relevant token.

    - `Logprobs param.Field[bool]`

      Whether to return log probabilities of the output tokens or not. If true,
      returns the log probabilities of each output token returned in the
      `content` of `message`.

    - `MaxCompletionTokens param.Field[int64]`

      An upper bound for the number of tokens that can be generated for a completion, including visible output tokens and [reasoning tokens](https://platform.openai.com/docs/guides/reasoning).

    - `MaxTokens param.Field[int64]`

      The maximum number of [tokens](/tokenizer) that can be generated in the
      chat completion. This value can be used to control
      [costs](https://openai.com/api/pricing/) for text generated via API.

      This value is now deprecated in favor of `max_completion_tokens`, and is
      not compatible with [o-series models](https://platform.openai.com/docs/guides/reasoning).

    - `Metadata param.Field[Metadata]`

      Set of 16 key-value pairs that can be attached to an object. This can be
      useful for storing additional information about the object in a structured
      format, and querying for objects via API or the dashboard.

      Keys are strings with a maximum length of 64 characters. Values are strings
      with a maximum length of 512 characters.

    - `Modalities param.Field[[]string]`

      Output types that you would like the model to generate.
      Most models are capable of generating text, which is the default:

      `["text"]`

      The `gpt-4o-audio-preview` model can also be used to
      [generate audio](https://platform.openai.com/docs/guides/audio). To request that this model generate
      both text and audio responses, you can use:

      `["text", "audio"]`

        - `const ChatCompletionNewParamsModalityText ChatCompletionNewParamsModality = "text"`

        - `const ChatCompletionNewParamsModalityAudio ChatCompletionNewParamsModality = "audio"`

    - `N param.Field[int64]`

      How many chat completion choices to generate for each input message. Note that you will be charged based on the number of generated tokens across all of the choices. Keep `n` as `1` to minimize costs.

    - `ParallelToolCalls param.Field[bool]`

      Whether to enable [parallel function calling](https://platform.openai.com/docs/guides/function-calling#configuring-parallel-function-calling) during tool use.

    - `Prediction param.Field[ChatCompletionPredictionContent]`

      Static predicted output content, such as the content of a text file that is
      being regenerated.

    - `PresencePenalty param.Field[float64]`

      Number between -2.0 and 2.0. Positive values penalize new tokens based on
      whether they appear in the text so far, increasing the model's likelihood
      to talk about new topics.

    - `PromptCacheKey param.Field[string]`

      Used by OpenAI to cache responses for similar requests to optimize your cache hit rates. Replaces the `user` field. [Learn more](https://platform.openai.com/docs/guides/prompt-caching).

    - `PromptCacheRetention param.Field[ChatCompletionNewParamsPromptCacheRetention]`

      The retention policy for the prompt cache. Set to `24h` to enable extended prompt caching, which keeps cached prefixes active for longer, up to a maximum of 24 hours. [Learn more](https://platform.openai.com/docs/guides/prompt-caching#prompt-cache-retention).

        - `const ChatCompletionNewParamsPromptCacheRetentionInMemory ChatCompletionNewParamsPromptCacheRetention = "in_memory"`

        - `const ChatCompletionNewParamsPromptCacheRetention24h ChatCompletionNewParamsPromptCacheRetention = "24h"`

    - `ReasoningEffort param.Field[ReasoningEffort]`

      Constrains effort on reasoning for
      [reasoning models](https://platform.openai.com/docs/guides/reasoning).
      Currently supported values are `none`, `minimal`, `low`, `medium`, `high`, and `xhigh`. Reducing
      reasoning effort can result in faster responses and fewer tokens used
      on reasoning in a response.

        - `gpt-5.1` defaults to `none`, which does not perform reasoning. The supported reasoning values for `gpt-5.1` are `none`, `low`, `medium`, and `high`. Tool calls are supported for all reasoning values in gpt-5.1.
        - All models before `gpt-5.1` default to `medium` reasoning effort, and do not support `none`.
        - The `gpt-5-pro` model defaults to (and only supports) `high` reasoning effort.
        - `xhigh` is supported for all models after `gpt-5.1-codex-max`.

    - `ResponseFormat param.Field[ChatCompletionNewParamsResponseFormatUnion]`

      An object specifying the format that the model must output.

      Setting to `{ "type": "json_schema", "json_schema": {...} }` enables
      Structured Outputs which ensures the model will match your supplied JSON
      schema. Learn more in the [Structured Outputs
      guide](https://platform.openai.com/docs/guides/structured-outputs).

      Setting to `{ "type": "json_object" }` enables the older JSON mode, which
      ensures the message the model generates is valid JSON. Using `json_schema`
      is preferred for models that support it.

        - `type ResponseFormatText struct{…}`

          Default response format. Used to generate text responses.

            - `Type Text`

              The type of response format being defined. Always `text`.

                - `const TextText Text = "text"`

        - `type ResponseFormatJSONSchema struct{…}`

          JSON Schema response format. Used to generate structured JSON responses.
          Learn more about [Structured Outputs](https://platform.openai.com/docs/guides/structured-outputs).

            - `JSONSchema ResponseFormatJSONSchemaJSONSchema`

              Structured Outputs configuration options, including a JSON Schema.

                - `Name string`

                  The name of the response format. Must be a-z, A-Z, 0-9, or contain
                  underscores and dashes, with a maximum length of 64.

                - `Description string`

                  A description of what the response format is for, used by the model to
                  determine how to respond in the format.

                - `Schema map[string, any]`

                  The schema for the response format, described as a JSON Schema object.
                  Learn how to build JSON schemas [here](https://json-schema.org/).

                - `Strict bool`

                  Whether to enable strict schema adherence when generating the output.
                  If set to true, the model will always follow the exact schema defined
                  in the `schema` field. Only a subset of JSON Schema is supported when
                  `strict` is `true`. To learn more, read the [Structured Outputs
                  guide](https://platform.openai.com/docs/guides/structured-outputs).

            - `Type JSONSchema`

              The type of response format being defined. Always `json_schema`.

                - `const JSONSchemaJSONSchema JSONSchema = "json_schema"`

        - `type ResponseFormatJSONObject struct{…}`

          JSON object response format. An older method of generating JSON responses.
          Using `json_schema` is recommended for models that support it. Note that the
          model will not generate JSON without a system or user message instructing it
          to do so.

            - `Type JSONObject`

              The type of response format being defined. Always `json_object`.

                - `const JSONObjectJSONObject JSONObject = "json_object"`

    - `SafetyIdentifier param.Field[string]`

      A stable identifier used to help detect users of your application that may be violating OpenAI's usage policies.
      The IDs should be a string that uniquely identifies each user, with a maximum length of 64 characters. We recommend hashing their username or email address, in order to avoid sending us any identifying information. [Learn more](https://platform.openai.com/docs/guides/safety-best-practices#safety-identifiers).

    - `Seed param.Field[int64]`

      This feature is in Beta.
      If specified, our system will make a best effort to sample deterministically, such that repeated requests with the same `seed` and parameters should return the same result.
      Determinism is not guaranteed, and you should refer to the `system_fingerprint` response parameter to monitor changes in the backend.

    - `ServiceTier param.Field[ChatCompletionNewParamsServiceTier]`

      Specifies the processing type used for serving the request.

        - If set to 'auto', then the request will be processed with the service tier configured in the Project settings. Unless otherwise configured, the Project will use 'default'.
        - If set to 'default', then the request will be processed with the standard pricing and performance for the selected model.
        - If set to '[flex](https://platform.openai.com/docs/guides/flex-processing)' or '[priority](https://openai.com/api-priority-processing/)', then the request will be processed with the corresponding service tier.
        - When not set, the default behavior is 'auto'.

      When the `service_tier` parameter is set, the response body will include the `service_tier` value based on the processing mode actually used to serve the request. This response value may be different from the value set in the parameter.

        - `const ChatCompletionNewParamsServiceTierAuto ChatCompletionNewParamsServiceTier = "auto"`

        - `const ChatCompletionNewParamsServiceTierDefault ChatCompletionNewParamsServiceTier = "default"`

        - `const ChatCompletionNewParamsServiceTierFlex ChatCompletionNewParamsServiceTier = "flex"`

        - `const ChatCompletionNewParamsServiceTierScale ChatCompletionNewParamsServiceTier = "scale"`

        - `const ChatCompletionNewParamsServiceTierPriority ChatCompletionNewParamsServiceTier = "priority"`

    - `Stop param.Field[ChatCompletionNewParamsStopUnion]`

      Not supported with latest reasoning models `o3` and `o4-mini`.

      Up to 4 sequences where the API will stop generating further tokens. The
      returned text will not contain the stop sequence.

        - `string`

        - `[]string`

    - `Store param.Field[bool]`

      Whether or not to store the output of this chat completion request for
      use in our [model distillation](https://platform.openai.com/docs/guides/distillation) or
      [evals](https://platform.openai.com/docs/guides/evals) products.

      Supports text and image inputs. Note: image inputs over 8MB will be dropped.

    - ``

    - `StreamOptions param.Field[ChatCompletionStreamOptions]`

      Options for streaming response. Only set this when you set `stream: true`.

    - `Temperature param.Field[float64]`

      What sampling temperature to use, between 0 and 2. Higher values like 0.8 will make the output more random, while lower values like 0.2 will make it more focused and deterministic.
      We generally recommend altering this or `top_p` but not both.

    - `ToolChoice param.Field[ChatCompletionToolChoiceOptionUnion]`

      Controls which (if any) tool is called by the model.
      `none` means the model will not call any tool and instead generates a message.
      `auto` means the model can pick between generating a message or calling one or more tools.
      `required` means the model must call one or more tools.
      Specifying a particular tool via `{"type": "function", "function": {"name": "my_function"}}` forces the model to call that tool.

      `none` is the default when no tools are present. `auto` is the default if tools are present.

    - `Tools param.Field[[]ChatCompletionToolUnion]`

      A list of tools the model may call. You can provide either
      [custom tools](https://platform.openai.com/docs/guides/function-calling#custom-tools) or
      [function tools](https://platform.openai.com/docs/guides/function-calling).

        - `type ChatCompletionFunctionTool struct{…}`

          A function tool that can be used to generate a response.

            - `Function FunctionDefinition`

                - `Name string`

                  The name of the function to be called. Must be a-z, A-Z, 0-9, or contain underscores and dashes, with a maximum length of 64.

                - `Description string`

                  A description of what the function does, used by the model to choose when and how to call the function.

                - `Parameters FunctionParameters`

                  The parameters the functions accepts, described as a JSON Schema object. See the [guide](https://platform.openai.com/docs/guides/function-calling) for examples, and the [JSON Schema reference](https://json-schema.org/understanding-json-schema/) for documentation about the format.

                  Omitting `parameters` defines a function with an empty parameter list.

                - `Strict bool`

                  Whether to enable strict schema adherence when generating the function call. If set to true, the model will follow the exact schema defined in the `parameters` field. Only a subset of JSON Schema is supported when `strict` is `true`. Learn more about Structured Outputs in the [function calling guide](https://platform.openai.com/docs/guides/function-calling).

            - `Type Function`

              The type of the tool. Currently, only `function` is supported.

                - `const FunctionFunction Function = "function"`

        - `type ChatCompletionCustomTool struct{…}`

          A custom tool that processes input using a specified format.

            - `Custom ChatCompletionCustomToolCustom`

              Properties of the custom tool.

                - `Name string`

                  The name of the custom tool, used to identify it in tool calls.

                - `Description string`

                  Optional description of the custom tool, used to provide more context.

                - `Format ChatCompletionCustomToolCustomFormatUnion`

                  The input format for the custom tool. Default is unconstrained text.

                    - `ChatCompletionCustomToolCustomFormatText`

                        - `Type Text`

                          Unconstrained text format. Always `text`.

                            - `const TextText Text = "text"`

                    - `ChatCompletionCustomToolCustomFormatGrammar`

                        - `Grammar ChatCompletionCustomToolCustomFormatGrammarGrammar`

                          Your chosen grammar.

                            - `Definition string`

                              The grammar definition.

                            - `Syntax string`

                              The syntax of the grammar definition. One of `lark` or `regex`.

                                - `const ChatCompletionCustomToolCustomFormatGrammarGrammarSyntaxLark ChatCompletionCustomToolCustomFormatGrammarGrammarSyntax = "lark"`

                                - `const ChatCompletionCustomToolCustomFormatGrammarGrammarSyntaxRegex ChatCompletionCustomToolCustomFormatGrammarGrammarSyntax = "regex"`

                        - `Type Grammar`

                          Grammar format. Always `grammar`.

                            - `const GrammarGrammar Grammar = "grammar"`

            - `Type Custom`

              The type of the custom tool. Always `custom`.

                - `const CustomCustom Custom = "custom"`

    - `TopLogprobs param.Field[int64]`

      An integer between 0 and 20 specifying the maximum number of most likely
      tokens to return at each token position, each with an associated log
      probability. In some cases, the number of returned tokens may be fewer than
      requested.
      `logprobs` must be set to `true` if this parameter is used.

    - `TopP param.Field[float64]`

      An alternative to sampling with temperature, called nucleus sampling,
      where the model considers the results of the tokens with top_p probability
      mass. So 0.1 means only the tokens comprising the top 10% probability mass
      are considered.

      We generally recommend altering this or `temperature` but not both.

    - `User param.Field[string]`

      This field is being replaced by `safety_identifier` and `prompt_cache_key`. Use `prompt_cache_key` instead to maintain caching optimizations.
      A stable identifier for your end-users.
      Used to boost cache hit rates by better bucketing similar requests and  to help OpenAI detect and prevent abuse. [Learn more](https://platform.openai.com/docs/guides/safety-best-practices#safety-identifiers).

    - `Verbosity param.Field[ChatCompletionNewParamsVerbosity]`

      Constrains the verbosity of the model's response. Lower values will result in
      more concise responses, while higher values will result in more verbose responses.
      Currently supported values are `low`, `medium`, and `high`.

        - `const ChatCompletionNewParamsVerbosityLow ChatCompletionNewParamsVerbosity = "low"`

        - `const ChatCompletionNewParamsVerbosityMedium ChatCompletionNewParamsVerbosity = "medium"`

        - `const ChatCompletionNewParamsVerbosityHigh ChatCompletionNewParamsVerbosity = "high"`

    - `WebSearchOptions param.Field[ChatCompletionNewParamsWebSearchOptions]`

      This tool searches the web for relevant results to use in a response.
      Learn more about the [web search tool](https://platform.openai.com/docs/guides/tools-web-search?api-mode=chat).

        - `SearchContextSize string`

          High level guidance for the amount of context window space to use for the
          search. One of `low`, `medium`, or `high`. `medium` is the default.

            - `const ChatCompletionNewParamsWebSearchOptionsSearchContextSizeLow ChatCompletionNewParamsWebSearchOptionsSearchContextSize = "low"`

            - `const ChatCompletionNewParamsWebSearchOptionsSearchContextSizeMedium ChatCompletionNewParamsWebSearchOptionsSearchContextSize = "medium"`

            - `const ChatCompletionNewParamsWebSearchOptionsSearchContextSizeHigh ChatCompletionNewParamsWebSearchOptionsSearchContextSize = "high"`

        - `UserLocation ChatCompletionNewParamsWebSearchOptionsUserLocation`

          Approximate location parameters for the search.

            - `Approximate ChatCompletionNewParamsWebSearchOptionsUserLocationApproximate`

              Approximate location parameters for the search.

                - `City string`

                  Free text input for the city of the user, e.g. `San Francisco`.

                - `Country string`

                  The two-letter
                  [ISO country code](https://en.wikipedia.org/wiki/ISO_3166-1) of the user,
                  e.g. `US`.

                - `Region string`

                  Free text input for the region of the user, e.g. `California`.

                - `Timezone string`

                  The [IANA timezone](https://timeapi.io/documentation/iana-timezones)
                  of the user, e.g. `America/Los_Angeles`.

            - `Type Approximate`

              The type of location approximation. Always `approximate`.

                - `const ApproximateApproximate Approximate = "approximate"`

### Returns

- `type ChatCompletion struct{…}`

  Represents a chat completion response returned by model, based on the provided input.

    - `ID string`

      A unique identifier for the chat completion.

    - `Choices []ChatCompletionChoice`

      A list of chat completion choices. Can be more than one if `n` is greater than 1.

        - `FinishReason string`

          The reason the model stopped generating tokens. This will be `stop` if the model hit a natural stop point or a provided stop sequence,
          `length` if the maximum number of tokens specified in the request was reached,
          `content_filter` if content was omitted due to a flag from our content filters,
          `tool_calls` if the model called a tool, or `function_call` (deprecated) if the model called a function.

            - `const ChatCompletionChoiceFinishReasonStop ChatCompletionChoiceFinishReason = "stop"`

            - `const ChatCompletionChoiceFinishReasonLength ChatCompletionChoiceFinishReason = "length"`

            - `const ChatCompletionChoiceFinishReasonToolCalls ChatCompletionChoiceFinishReason = "tool_calls"`

            - `const ChatCompletionChoiceFinishReasonContentFilter ChatCompletionChoiceFinishReason = "content_filter"`

            - `const ChatCompletionChoiceFinishReasonFunctionCall ChatCompletionChoiceFinishReason = "function_call"`

        - `Index int64`

          The index of the choice in the list of choices.

        - `Logprobs ChatCompletionChoiceLogprobs`

          Log probability information for the choice.

            - `Content []ChatCompletionTokenLogprob`

              A list of message content tokens with log probability information.

                - `Token string`

                  The token.

                - `Bytes []int64`

                  A list of integers representing the UTF-8 bytes representation of the token. Useful in instances where characters are represented by multiple tokens and their byte representations must be combined to generate the correct text representation. Can be `null` if there is no bytes representation for the token.

                - `Logprob float64`

                  The log probability of this token, if it is within the top 20 most likely tokens. Otherwise, the value `-9999.0` is used to signify that the token is very unlikely.

                - `TopLogprobs []ChatCompletionTokenLogprobTopLogprob`

                  List of the most likely tokens and their log probability, at this token position. The number of entries may be fewer than the requested `top_logprobs`.

                    - `Token string`

                      The token.

                    - `Bytes []int64`

                      A list of integers representing the UTF-8 bytes representation of the token. Useful in instances where characters are represented by multiple tokens and their byte representations must be combined to generate the correct text representation. Can be `null` if there is no bytes representation for the token.

                    - `Logprob float64`

                      The log probability of this token, if it is within the top 20 most likely tokens. Otherwise, the value `-9999.0` is used to signify that the token is very unlikely.

            - `Refusal []ChatCompletionTokenLogprob`

              A list of message refusal tokens with log probability information.

                - `Token string`

                  The token.

                - `Bytes []int64`

                  A list of integers representing the UTF-8 bytes representation of the token. Useful in instances where characters are represented by multiple tokens and their byte representations must be combined to generate the correct text representation. Can be `null` if there is no bytes representation for the token.

                - `Logprob float64`

                  The log probability of this token, if it is within the top 20 most likely tokens. Otherwise, the value `-9999.0` is used to signify that the token is very unlikely.

                - `TopLogprobs []ChatCompletionTokenLogprobTopLogprob`

                  List of the most likely tokens and their log probability, at this token position. The number of entries may be fewer than the requested `top_logprobs`.

        - `Message ChatCompletionMessage`

          A chat completion message generated by the model.

            - `Content string`

              The contents of the message.

            - `Refusal string`

              The refusal message generated by the model.

            - `Role Assistant`

              The role of the author of this message.

                - `const AssistantAssistant Assistant = "assistant"`

            - `Annotations []ChatCompletionMessageAnnotation`

              Annotations for the message, when applicable, as when using the
              [web search tool](https://platform.openai.com/docs/guides/tools-web-search?api-mode=chat).

                - `Type URLCitation`

                  The type of the URL citation. Always `url_citation`.

                    - `const URLCitationURLCitation URLCitation = "url_citation"`

                - `URLCitation ChatCompletionMessageAnnotationURLCitation`

                  A URL citation when using web search.

                    - `EndIndex int64`

                      The index of the last character of the URL citation in the message.

                    - `StartIndex int64`

                      The index of the first character of the URL citation in the message.

                    - `Title string`

                      The title of the web resource.

                    - `URL string`

                      The URL of the web resource.

            - `Audio ChatCompletionAudio`

              If the audio output modality is requested, this object contains data
              about the audio response from the model. [Learn more](https://platform.openai.com/docs/guides/audio).

                - `ID string`

                  Unique identifier for this audio response.

                - `Data string`

                  Base64 encoded audio bytes generated by the model, in the format
                  specified in the request.

                - `ExpiresAt int64`

                  The Unix timestamp (in seconds) for when this audio response will
                  no longer be accessible on the server for use in multi-turn
                  conversations.

                - `Transcript string`

                  Transcript of the audio generated by the model.

            - `FunctionCall ChatCompletionMessageFunctionCall`

              Deprecated and replaced by `tool_calls`. The name and arguments of a function that should be called, as generated by the model.

                - `Arguments string`

                  The arguments to call the function with, as generated by the model in JSON format. Note that the model does not always generate valid JSON, and may hallucinate parameters not defined by your function schema. Validate the arguments in your code before calling your function.

                - `Name string`

                  The name of the function to call.

            - `ToolCalls []ChatCompletionMessageToolCallUnion`

              The tool calls generated by the model, such as function calls.

                - `type ChatCompletionMessageFunctionToolCall struct{…}`

                  A call to a function tool created by the model.

                    - `ID string`

                      The ID of the tool call.

                    - `Function ChatCompletionMessageFunctionToolCallFunction`

                      The function that the model called.

                        - `Arguments string`

                          The arguments to call the function with, as generated by the model in JSON format. Note that the model does not always generate valid JSON, and may hallucinate parameters not defined by your function schema. Validate the arguments in your code before calling your function.

                        - `Name string`

                          The name of the function to call.

                    - `Type Function`

                      The type of the tool. Currently, only `function` is supported.

                        - `const FunctionFunction Function = "function"`

                - `type ChatCompletionMessageCustomToolCall struct{…}`

                  A call to a custom tool created by the model.

                    - `ID string`

                      The ID of the tool call.

                    - `Custom ChatCompletionMessageCustomToolCallCustom`

                      The custom tool that the model called.

                        - `Input string`

                          The input for the custom tool call generated by the model.

                        - `Name string`

                          The name of the custom tool to call.

                    - `Type Custom`

                      The type of the tool. Always `custom`.

                        - `const CustomCustom Custom = "custom"`

    - `Created int64`

      The Unix timestamp (in seconds) of when the chat completion was created.

    - `Model string`

      The model used for the chat completion.

    - `Object ChatCompletion`

      The object type, which is always `chat.completion`.

        - `const ChatCompletionChatCompletion ChatCompletion = "chat.completion"`

    - `ServiceTier ChatCompletionServiceTier`

      Specifies the processing type used for serving the request.

        - If set to 'auto', then the request will be processed with the service tier configured in the Project settings. Unless otherwise configured, the Project will use 'default'.
        - If set to 'default', then the request will be processed with the standard pricing and performance for the selected model.
        - If set to '[flex](https://platform.openai.com/docs/guides/flex-processing)' or '[priority](https://openai.com/api-priority-processing/)', then the request will be processed with the corresponding service tier.
        - When not set, the default behavior is 'auto'.

      When the `service_tier` parameter is set, the response body will include the `service_tier` value based on the processing mode actually used to serve the request. This response value may be different from the value set in the parameter.

        - `const ChatCompletionServiceTierAuto ChatCompletionServiceTier = "auto"`

        - `const ChatCompletionServiceTierDefault ChatCompletionServiceTier = "default"`

        - `const ChatCompletionServiceTierFlex ChatCompletionServiceTier = "flex"`

        - `const ChatCompletionServiceTierScale ChatCompletionServiceTier = "scale"`

        - `const ChatCompletionServiceTierPriority ChatCompletionServiceTier = "priority"`

    - `SystemFingerprint string`

      This fingerprint represents the backend configuration that the model runs with.

      Can be used in conjunction with the `seed` request parameter to understand when backend changes have been made that might impact determinism.

    - `Usage CompletionUsage`

      Usage statistics for the completion request.

        - `CompletionTokens int64`

          Number of tokens in the generated completion.

        - `PromptTokens int64`

          Number of tokens in the prompt.

        - `TotalTokens int64`

          Total number of tokens used in the request (prompt + completion).

        - `CompletionTokensDetails CompletionUsageCompletionTokensDetails`

          Breakdown of tokens used in a completion.

            - `AcceptedPredictionTokens int64`

              When using Predicted Outputs, the number of tokens in the
              prediction that appeared in the completion.

            - `AudioTokens int64`

              Audio input tokens generated by the model.

            - `ReasoningTokens int64`

              Tokens generated by the model for reasoning.

            - `RejectedPredictionTokens int64`

              When using Predicted Outputs, the number of tokens in the
              prediction that did not appear in the completion. However, like
              reasoning tokens, these tokens are still counted in the total
              completion tokens for purposes of billing, output, and context window
              limits.

        - `PromptTokensDetails CompletionUsagePromptTokensDetails`

          Breakdown of tokens used in the prompt.

            - `AudioTokens int64`

              Audio input tokens present in the prompt.

            - `CachedTokens int64`

              Cached tokens present in the prompt.

### Example

```go
package main

import (
  "context"
  "fmt"

  "github.com/openai/openai-go"
  "github.com/openai/openai-go/option"
  "github.com/openai/openai-go/shared"
)

func main() {
  client := openai.NewClient(
    option.WithAPIKey("My API Key"),
  )
  chatCompletion, err := client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
    Messages: []openai.ChatCompletionMessageParamUnion{openai.ChatCompletionMessageParamUnion{
      OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
        Content: openai.ChatCompletionDeveloperMessageParamContentUnion{
          OfString: openai.String("string"),
        },
      },
    }},
    Model: shared.ChatModelGPT5_4,
  })
  if err != nil {
    panic(err.Error())
  }
  fmt.Printf("%+v\n", chatCompletion)
}
```

#### Response

```json
{
  "id": "id",
  "choices": [
    {
      "finish_reason": "stop",
      "index": 0,
      "logprobs": {
        "content": [
          {
            "token": "token",
            "bytes": [
              0
            ],
            "logprob": 0,
            "top_logprobs": [
              {
                "token": "token",
                "bytes": [
                  0
                ],
                "logprob": 0
              }
            ]
          }
        ],
        "refusal": [
          {
            "token": "token",
            "bytes": [
              0
            ],
            "logprob": 0,
            "top_logprobs": [
              {
                "token": "token",
                "bytes": [
                  0
                ],
                "logprob": 0
              }
            ]
          }
        ]
      },
      "message": {
        "content": "content",
        "refusal": "refusal",
        "role": "assistant",
        "annotations": [
          {
            "type": "url_citation",
            "url_citation": {
              "end_index": 0,
              "start_index": 0,
              "title": "title",
              "url": "https://example.com"
            }
          }
        ],
        "audio": {
          "id": "id",
          "data": "data",
          "expires_at": 0,
          "transcript": "transcript"
        },
        "function_call": {
          "arguments": "arguments",
          "name": "name"
        },
        "tool_calls": [
          {
            "id": "id",
            "function": {
              "arguments": "arguments",
              "name": "name"
            },
            "type": "function"
          }
        ]
      }
    }
  ],
  "created": 0,
  "model": "model",
  "object": "chat.completion",
  "service_tier": "auto",
  "system_fingerprint": "system_fingerprint",
  "usage": {
    "completion_tokens": 0,
    "prompt_tokens": 0,
    "total_tokens": 0,
    "completion_tokens_details": {
      "accepted_prediction_tokens": 0,
      "audio_tokens": 0,
      "reasoning_tokens": 0,
      "rejected_prediction_tokens": 0
    },
    "prompt_tokens_details": {
      "audio_tokens": 0,
      "cached_tokens": 0
    }
  }
}
```
