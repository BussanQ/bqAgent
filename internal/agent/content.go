package agent

import "bqagent/internal/media"

// ImageAttachment is a decoded inbound image to be sent to the model as part of
// a multimodal user message. Data holds the raw image bytes; the runtime encodes
// it into a base64 data URI (OpenAI `image_url`) when building the message.
type ImageAttachment = media.Image
