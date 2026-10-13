package chatgpt

import "github.com/Autumn-27/artex/locale"

func init() {
	for en, ko := range map[string]string{
		"invalid account registration":                                               "잘못된 계정 등록입니다",
		"authorization attempt already completed":                                    "이미 완료된 승인 시도입니다",
		"OAuth callback must be http://127.0.0.1:PORT/auth/callback":                 "OAuth 콜백은 http://127.0.0.1:PORT/auth/callback이어야 합니다",
		"unexpected callback address":                                                "예상하지 않은 콜백 주소입니다",
		"invalid OAuth callback":                                                     "잘못된 OAuth 콜백입니다",
		"duplicate OAuth callback parameter":                                         "중복된 OAuth 콜백 매개변수입니다",
		"OAuth state mismatch":                                                       "OAuth state가 일치하지 않습니다",
		"ChatGPT authorization was declined or failed":                               "ChatGPT 승인이 거부되었거나 실패했습니다",
		"OAuth client ID mismatch":                                                   "OAuth 클라이언트 ID가 일치하지 않습니다",
		"incomplete ChatGPT client registration":                                     "ChatGPT 클라이언트 등록이 완료되지 않았습니다",
		"ChatGPT account identity changed; use a different account label":            "ChatGPT 계정 신원이 변경되었습니다. 다른 계정 이름을 사용하세요",
		"could not build OAuth request":                                              "OAuth 요청을 생성할 수 없습니다",
		"could not reach ChatGPT token endpoint":                                     "ChatGPT 토큰 엔드포인트에 연결할 수 없습니다",
		"invalid ChatGPT token response":                                             "잘못된 ChatGPT 토큰 응답입니다",
		"incomplete ChatGPT token response":                                          "ChatGPT 토큰 응답이 불완전합니다",
		"ChatGPT plan usage was not authorized; sign in again and grant plan access": "ChatGPT 플랜 사용이 승인되지 않았습니다. 다시 로그인하고 플랜 접근을 승인하세요",
		"invalid refresh time":                                                       "잘못된 갱신 시간입니다",
		"invalid OpenAI discovery endpoint":                                          "잘못된 OpenAI 검색 엔드포인트입니다",
		"could not reach OpenAI discovery endpoint":                                  "OpenAI 검색 엔드포인트에 연결할 수 없습니다",
		"OpenAI discovery request failed":                                            "OpenAI 검색 요청이 실패했습니다",
		"invalid OpenAI discovery response":                                          "잘못된 OpenAI 검색 응답입니다",
		"OpenAI discovery issuer mismatch":                                           "OpenAI 검색 발급자가 일치하지 않습니다",
		"untrusted OpenAI discovery endpoint":                                        "신뢰할 수 없는 OpenAI 검색 엔드포인트입니다",
		"invalid signing key":                                                        "잘못된 서명 키입니다",
		"invalid signing exponent":                                                   "잘못된 서명 지수입니다",
		"OpenAI signing key was not found":                                           "OpenAI 서명 키를 찾을 수 없습니다",
		"ChatGPT ID token validation failed":                                         "ChatGPT ID 토큰 검증이 실패했습니다",
		"ChatGPT sign-in required":                                                   "ChatGPT 로그인이 필요합니다",
		"ChatGPT token cannot be refreshed yet":                                      "아직 ChatGPT 토큰을 갱신할 수 없습니다",
		"ChatGPT session expired and could not be cleared from disk":                 "ChatGPT 세션이 만료되었으며 디스크에서 삭제할 수 없습니다",
		"refreshed ChatGPT identity validation failed; sign in again":                "갱신된 ChatGPT 신원 검증이 실패했습니다. 다시 로그인하세요",
		"could not persist renewed ChatGPT credentials; sign in again":               "갱신된 ChatGPT 자격 증명을 저장할 수 없습니다. 다시 로그인하세요",
		"remote ChatGPT revocation was not confirmed":                                "원격 ChatGPT 해지를 확인하지 못했습니다",
		"could not clear local ChatGPT credentials":                                  "로컬 ChatGPT 자격 증명을 삭제할 수 없습니다",
		"local tokens cleared; remote revocation was not confirmed; disconnect the app in ChatGPT Settings": "로컬 토큰이 삭제되었지만 원격 해지를 확인하지 못했습니다. ChatGPT 설정에서 앱 연결을 해제하세요",
		"credential directory must be a real directory":                                                     "자격 증명 디렉터리는 실제 디렉터리여야 합니다",
		"ChatGPT credentials are in use; stop the other bridge/login process first":                         "ChatGPT 자격 증명이 사용 중입니다. 다른 브리지/로그인 프로세스를 먼저 중지하세요",
		"account label must contain 1-64 letters, digits, underscores or hyphens":                           "계정 이름은 영문자, 숫자, 밑줄 또는 하이픈 1~64자로 구성해야 합니다",
		"credential file must be a regular file":                                                            "자격 증명 파일은 일반 파일이어야 합니다",
		"invalid credential file":                                                                           "잘못된 자격 증명 파일입니다",
		"invalid host ID":                                                                                   "잘못된 호스트 ID입니다",
		"invalid local bridge key":                                                                          "잘못된 로컬 브리지 키입니다",
		"local bridge key is required":                                                                      "로컬 브리지 키가 필요합니다",
		"bridge must listen on a literal loopback IP address":                                               "브리지는 명시적인 루프백 IP 주소에서 수신해야 합니다",
		"OpenAI inference endpoint unavailable":                                                             "OpenAI 추론 엔드포인트를 사용할 수 없습니다",
		"ChatGPT OAuth failed: HTTP %d (%s)":                                                                "ChatGPT OAuth 실패: HTTP %d (%s)",
	} {
		locale.Register(en, en, ko)
	}
}

func (e *OAuthError) MessageForLanguage(lang locale.Lang) string {
	return locale.Text(lang, "ChatGPT OAuth failed: HTTP %d (%s)", e.Status, e.Code)
}
