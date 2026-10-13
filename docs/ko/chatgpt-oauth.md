# ChatGPT OAuth 브리지

[English](../chatgpt-oauth.md)

이 선택적 브리지는 로컬 ScopeWeaver가 사용자가 승인한 ChatGPT 플랜의 사용 가능한
모델을 이용하도록 합니다. OpenAI의 공식 **Sign in with ChatGPT** 공개 클라이언트
흐름을 구현합니다. Codex 로그인 파일을 가져오거나 다른 앱을 사칭하거나 비공개
ChatGPT 엔드포인트를 사용하지 않습니다. 기존 플래너, 워커, 작업, MCP 어댑터,
프로필 선택 및 API 키 공급자는 변경하지 않습니다. 기존 사용 제한도 계속 적용됩니다.

기존 `openai-responses` 프로필을 공식 Responses 엔드포인트에 연결합니다.
ScopeWeaver가 도구 실행과 오케스트레이션을 담당합니다. 공급자 통합이며 새로운
다중 에이전트 스케줄러가 아닙니다. 기존 작업/에이전트 프로필 바인딩으로 역할별
모델을 지정할 수 있습니다. 프로필 체인은 모델 합의가 아닌 장애 전환을 제공합니다.

## 빌드 및 로그인

Go 백엔드와 동일한 WSL 인스턴스에서 실행합니다.

```bash
cd /home/emathioni/scopeweaver
mkdir -p dist
go build -o dist/scopeweaver-chatgpt ./cmd/scopeweaver-chatgpt
./dist/scopeweaver-chatgpt login
```

CLI가 출력하는 **Continue with ChatGPT** 승인 URL을 시스템 브라우저에서 열고,
계정/워크스페이스를 선택한 뒤 앱과 플랜 사용을 승인합니다. 브라우저는
`http://127.0.0.1:1455/auth/callback`으로 돌아옵니다.

**Windows + WSL:** Windows가 WSL 루프백 리스너에 연결하지 못하면 승인 후 연결
오류가 표시될 수 있습니다. 주소 표시줄의 전체 콜백 URL을 대기 중인 CLI에
붙여넣고 Enter를 누르세요. HTTP 콜백과 동일하게 state, 정확한 리디렉션 URI,
PKCE, 서명된 ID 토큰, nonce, 계정 신원 및 승인 범위를 검증합니다. 콜백이나 승인
URL을 채팅, 로그 또는 이슈에 붙여넣지 마세요. 공식 OAuth 콜백은 `127.0.0.1`을
사용해야 하므로 `localhost`로 바꾸면 안 됩니다.

포트가 사용 중이면 다른 IPv4 루프백 포트를 선택하세요.

```bash
./dist/scopeweaver-chatgpt login --callback-listen 127.0.0.1:54321
```

승인 시도는 10분 후 만료됩니다. 코드 교환이 실패하면 로그인을 다시 시작하세요.
사용 가능 모델, 권한, 계정 자격 및 한도는 OpenAI가 결정합니다. 구독만으로 이
흐름이나 특정 모델의 사용 가능 여부가 보장되지는 않습니다.

실패 시 콜백에는 코드나 토큰을 노출하지 않는 진단 메시지가 표시됩니다.
ID 토큰 시간 검증은 30초의 시계 차이를 허용하지만 서명, 발급자, audience,
subject 및 nonce 검증은 유지합니다. 현재 로그인 명령에서 새로 출력한 승인 URL을
사용하세요.

## 프로필 추가 및 브리지 실행

브리지를 시작하기 전에 계정과 실제 모델 목록을 확인합니다.

```bash
./dist/scopeweaver-chatgpt status
./dist/scopeweaver-chatgpt models
./dist/scopeweaver-chatgpt profile --model MODEL_ID
```

`MODEL_ID`는 목록의 모델 `id`로 바꿉니다. 마지막 명령의 JSON에는 OAuth 토큰이
아닌 별도의 **로컬 브리지 키**가 포함됩니다. ScopeWeaver의 LLM 설정에서 다음
프로필을 만드세요.

| 설정 | 값 |
| --- | --- |
| 형식 | `openai-responses` |
| Base URL | `http://127.0.0.1:8789/v1` |
| API 키 | 생성된 JSON의 `api_key` |
| 모델 | 선택한 목록의 `id` |
| 스트리밍 | 켜짐 (원자적 비스트리밍 호출도 지원) |
| 최대 출력 토큰 | `0` (OAuth 경로는 출력 한도를 허용하지 않음) |

추가 모델/추론 수준 프로필도 같은 로컬 브리지 키를 사용합니다. API 키 필드를
비워 두면 기존 프로필을 편집할 때만 저장된 키가 유지됩니다. 새 프로필은 키를
상속하지 않습니다. 키는 자격 증명 디렉터리의 비공개 `bridge-key.json` 파일에
있습니다. 프로필 API 키 필드에 OAuth 토큰을 붙여넣지 마세요.

브리지를 실행한 상태로 유지합니다.

```bash
./dist/scopeweaver-chatgpt serve
```

기본 브리지 포트는 `8789`입니다. ScopeWeaver는 이미 트래픽 기록 프록시용으로
`8788`을 사용합니다.

ScopeWeaver의 기존 **연결 테스트**를 실행한 뒤 평소처럼 프로필을 선택/바인딩합니다.
DeepSeek는 활성 공급자나 대체 공급자로 유지할 수 있습니다. 전역 풀은 기존
장애 전환 설정이 활성화되어야 하며 명시적으로 바인딩한 프로필은 바인딩 프로필
대체 설정도 필요합니다. 명령은 작업을 자동으로 생성하거나 재개하지 않습니다.

브리지는 **백엔드**에서 접근할 수 있는 위치에 있어야 합니다. Windows 브라우저가
브리지에 접근할 필요는 없습니다. 위 구성에서는 백엔드와 브리지가 WSL에서 같은
네트워크 네임스페이스와 `127.0.0.1`을 공유합니다.

## 계정, 저장 및 로그아웃

계정/워크스페이스 등록마다 별도의 `--account NAME`을 사용하고 모든 명령에 같은
이름을 전달합니다. 등록마다 발급된 OAuth 클라이언트 ID와 검증된 신원을 유지하며
재로그인으로 다른 계정 신원을 덮어쓸 수 없습니다. 서로 다른 계정을 동시에
실행하려면 다른 `--directory`와 `--listen`을 사용하세요. 각 디렉터리는 별도의
호스트를 나타내며 재시작 후에도 유지해야 합니다.

기본 저장 위치는 사용자 설정 디렉터리의 `scopeweaver/chatgpt`이며 Linux에서는
`~/.config/scopeweaver/chatgpt`입니다. `--directory PATH`는 저장소 밖의 비공개
로컬 런타임 위치만 사용하세요. Unix 디렉터리는 `0700`, 원자적으로 쓰는 파일은
`0600`입니다. Windows 네이티브 빌드는 현재 사용자와 SYSTEM만 접근할 수 있는
보호된 디렉터리 ACL을 설정합니다. 공유 폴더나 공유 백업에 저장하지 마세요.
토큰은 ScopeWeaver 프로필, 브라우저 저장소 또는 브리지 HTTP API에 노출되지 않습니다.

한 프로세스만 자격 증명 디렉터리를 잠그고 토큰을 갱신할 수 있습니다.
login/status/models/profile/logout 전에 Ctrl+C로 `serve`를 중지하세요.
액세스 토큰 만료가 가까워지면 자동 갱신하며 액세스 토큰, 새 리프레시 토큰 및
만료 정보를 함께 저장합니다. 무효화된 grant는 재로그인이 필요합니다.
HTTP 401은 추론 내용 전송 전에 갱신 후 한 번만 재시도하며 부분 응답은 재실행하지 않습니다.

```bash
./dist/scopeweaver-chatgpt logout
```

로그아웃은 원격 해지를 시도하고 로컬 토큰을 삭제하지만 계정/클라이언트 매핑과
호스트 ID는 유지합니다. 원격 해지 확인 실패가 표시되면 ChatGPT 설정에서 앱
연결을 해제하세요. 설정 → Usage에서 앱 권한과 플랜/크레딧 한도를 확인할 수 있습니다.
한국어 CLI 안내는 `--lang ko`를 추가합니다.

Windows 바이너리는 `GOOS=windows GOARCH=amd64 go build -o
dist/scopeweaver-chatgpt.exe ./cmd/scopeweaver-chatgpt`로 빌드할 수 있습니다.
WSL 자격 증명 디렉터리가 아닌 Windows의 비공개 디렉터리를 사용하고 백엔드가
브리지 리스너에 연결 가능한지 확인하세요.

## 프로토콜 및 현재 제한

- 인증된 루프백 HTTP 클라이언트만 허용합니다. 공개 리스너, CORS, 로그인 HTTP API,
  임의 upstream URL 또는 별도 과금 OpenAI API 키로의 자동 대체는 없습니다.
- `/v1/models`는 계정별 표시 가능한 모델을 일반 OpenAI `data` 형식으로 반환합니다.
  `/v1/responses`는 ScopeWeaver의 상태 비저장 호출을 지원합니다.
  `/health`도 로컬 브리지 키로 보호됩니다.
- upstream에는 항상 `store:false`, `stream:true`, 전체 기록 및 namespace로 묶은
  로컬 함수 도구를 보냅니다. 함수 이름과 호출 ID는 유지하고 재전송하는 호출/결과에
  namespace를 추가합니다. 도구는 ScopeWeaver가 실행합니다.
- OAuth 경로에는 `max_output_tokens`와 `temperature`를 보내지 않습니다.
  서버 저장 상태 필드와 hosted 도구는 거부합니다. 현재 공급자의 텍스트와 함수
  호출을 지원하며 이미지, 오디오, 비디오 또는 hosted 도구를 추가하지 않습니다.
- 비스트리밍 호출자는 upstream 완료 후 최종 응답을 받습니다. 마지막 이벤트의
  output 배열이 비어 있으면 완료된 출력 항목으로 복원하며 Content-Type 헤더가
  없는 SSE도 허용합니다. 잘린 스트림, 실패
  및 미완료 응답은 오류가 됩니다. 출력 전 한도/인증 실패는 기존 풀에서 사용할
  HTTP 오류로 전달하고 부분 출력 후 오류는 호출 재실행 없이 전달합니다.
- Claude OAuth는 구현하지 않았으며 Claude API 키 프로필은 계속 사용할 수 있습니다.
- 로컬/open-source 사용을 대상으로 합니다. 상용/원격 서비스는 OpenAI의 별도
  온보딩 및 제품 요구 사항을 따라야 합니다.

## 검증

```bash
go test -race ./chatgpt ./cmd/scopeweaver-chatgpt -count=1
go vet ./chatgpt ./cmd/scopeweaver-chatgpt
```

테스트는 격리된 OAuth/JWKS/추론 서버, 서명된 fixture ID 토큰, 실제 ScopeWeaver
공급자 및 LLM 풀을 사용합니다. 신원/동의, state/PKCE, 기존 등록, 갱신 및 저장,
해지, 로컬 접근, 도구 기록, 비스트리밍, 안전한 장애 전환과 중단 스트림을 검사합니다.
특정 계정의 접근 여부는 실제 로그인과 ScopeWeaver 연결 테스트로 확인해야 합니다.

공식 참고 자료 (2026년 10월 13일 확인):
[개요](https://developers.openai.com/siwc/token-sharing-open-source),
[등록](https://developers.openai.com/siwc/token-sharing-open-source/sign-in),
[세션](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions),
[추론](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference),
[미리보기 제한](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).
