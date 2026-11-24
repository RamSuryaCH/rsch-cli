// Package chatgpt implements Sign in with ChatGPT for RSCH, so a user can run
// requests on their own ChatGPT plan without an API key.
//
// It follows OpenAI's public documentation only:
//   - https://developers.openai.com/cookbook/articles/sign-in-with-chatgpt
//   - https://developers.openai.com/siwc/token-sharing-open-source (sign-in,
//     models and inference, profiles and sessions, errors and recovery)
//
// The flow is dynamic client registration with PKCE through a 127.0.0.1
// loopback callback, then an authorization-code exchange, ID-token
// verification against the issuer's JWKS, refresh-token rotation and
// revocation on logout. Inference goes to the public Responses API.
//
// No code is taken from OpenAI's Sign-in with ChatGPT DevKit
// (github.com/openai/sign-in-with-chatgpt-devkit): it is published under a
// noncommercial licence that is not compatible with RSCH's MIT licence. The
// DevKit and its Paste Perfect example were read as a description of the
// documented behaviour only.
//
// No client ID or secret is built in. OpenAI issues a client ID to each
// installation during sign-in; it is stored with the user's tokens in the
// RSCH config directory with owner-only permissions.
package chatgpt
