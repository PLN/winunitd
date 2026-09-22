// Immediate MSI actions: change session properties only, never machine state.
// Deferred service actions receive the fresh transaction id in their command lines.
// msiexec /f ignores command-line properties. PullTestFail copies the disposable
// qualification hook from the launching msiexec process. The client sequence does
// that directly. The elevated sequence does not clear a value already set, and
// reads the launching process when the property is still empty so a silent repair
// still sees WINUNITD_TEST_FAIL. PrepareService is a deferred DLL action: it runs
// the embedded helper and copies the helper's standard error into the MSI log.
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <msi.h>
#include <msiquery.h>
#include <bcrypt.h>

static void memzero(void *dst, unsigned int n) {
	unsigned char *p = dst;
	while (n--)
		*p++ = 0;
}

static void logPlain(MSIHANDLE session, const wchar_t *text) {
	MSIHANDLE rec;
	if (!text || !text[0])
		return;
	rec = MsiCreateRecord(1);
	if (!rec)
		return;
	MsiRecordSetStringW(rec, 0, L"[1]");
	MsiRecordSetStringW(rec, 1, text);
	MsiProcessMessage(session, INSTALLMESSAGE_INFO, rec);
	MsiCloseHandle(rec);
}

static void logNumber(MSIHANDLE session, const wchar_t *text, unsigned long code) {
	wchar_t buf[160];
	DWORD i = 0;
	DWORD k = 0;
	wchar_t digits[16];
	DWORD n = 0;
	if (!text)
		return;
	while (text[k] && i + 1 < 128)
		buf[i++] = text[k++];
	buf[i++] = L' ';
	if (code == 0)
		digits[n++] = L'0';
	while (code && n < 16) {
		digits[n++] = (wchar_t)(L'0' + (code % 10));
		code /= 10;
	}
	while (n && i + 1 < 160)
		buf[i++] = digits[--n];
	buf[i] = 0;
	logPlain(session, buf);
}

static int hexToken(const wchar_t *token, DWORD n) {
	DWORD i;
	if (n != 32)
		return 0;
	for (i = 0; i < n; i++) {
		wchar_t c = token[i];
		if (!((c >= L'0' && c <= L'9') || (c >= L'a' && c <= L'f')))
			return 0;
	}
	return 1;
}

static HMODULE thisModule(void) {
	MEMORY_BASIC_INFORMATION info;
	memzero(&info, sizeof(info));
	if (!VirtualQuery((void *)thisModule, &info, sizeof(info)))
		return NULL;
	return (HMODULE)info.AllocationBase;
}

static int helperPath(wchar_t *dst, unsigned cap, const wchar_t *token) {
	HMODULE mod = thisModule();
	DWORD n;
	DWORD i;
	DWORD attrs;
	wchar_t slash;
	const wchar_t *name = L"winunitd-msi-";
	if (!mod || !hexToken(token, 32))
		return 0;
	n = GetModuleFileNameW(mod, dst, cap);
	if (n == 0 || n + 1 >= cap)
		return 0;
	i = n;
	while (i > 0 && dst[i - 1] != L'\\' && dst[i - 1] != L'/')
		i--;
	if (i == 0)
		return 0;
	slash = dst[i - 1];
	dst[i - 1] = 0;
	attrs = GetFileAttributesW(dst);
	dst[i - 1] = slash;
	if (attrs == INVALID_FILE_ATTRIBUTES || (attrs & FILE_ATTRIBUTE_REPARSE_POINT))
		return 0;
	while (*name) {
		if (i + 1 >= cap)
			return 0;
		dst[i++] = *name++;
	}
	while (*token) {
		if (i + 1 >= cap)
			return 0;
		dst[i++] = *token++;
	}
	name = L".exe";
	while (*name) {
		if (i + 1 >= cap)
			return 0;
		dst[i++] = *name++;
	}
	dst[i] = 0;
	return 1;
}

static UINT extractHelper(MSIHANDLE session) {
	wchar_t token[40];
	wchar_t *path = NULL;
	DWORD tokenLen = 40;
	MSIHANDLE db = 0;
	MSIHANDLE view = 0;
	MSIHANDLE rec = 0;
	HANDLE file = INVALID_HANDLE_VALUE;
	unsigned char *chunk = NULL;
	UINT result = ERROR_INSTALL_FAILURE;
	DWORD total = 0;
	int created = 0;
	unsigned long fail = 0;

	memzero(token, sizeof(token));
	path = HeapAlloc(GetProcessHeap(), HEAP_ZERO_MEMORY, 1024 * sizeof(wchar_t));
	if (!path) {
		logPlain(session, L"service helper could not be prepared");
		return ERROR_INSTALL_FAILURE;
	}
	if (MsiGetPropertyW(session, L"WinunitdPolicyTransaction", token, &tokenLen) != ERROR_SUCCESS || !hexToken(token, tokenLen) || !helperPath(path, 1024, token))
		goto done;
	db = MsiGetActiveDatabase(session);
	if (!db)
		goto done;
	if (MsiDatabaseOpenViewW(db, L"SELECT `Data` FROM `Binary` WHERE `Name`='ServiceHelper'", &view) != ERROR_SUCCESS)
		goto done;
	if (MsiViewExecute(view, 0) != ERROR_SUCCESS)
		goto done;
	if (MsiViewFetch(view, &rec) != ERROR_SUCCESS)
		goto done;
	file = CreateFileW(path, GENERIC_WRITE, 0, NULL, CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL);
	if (file == INVALID_HANDLE_VALUE) {
		fail = GetLastError();
		goto done;
	}
	created = 1;
	chunk = HeapAlloc(GetProcessHeap(), 0, 8192);
	if (!chunk)
		goto done;
	for (;;) {
		DWORD n = 8192;
		DWORD wrote = 0;
		UINT err = MsiRecordReadStream(rec, 1, (char *)chunk, &n);
		if (err != ERROR_SUCCESS) {
			fail = err;
			goto done;
		}
		if (n == 0)
			break;
		if (total == 0 && (n < 2 || chunk[0] != 'M' || chunk[1] != 'Z')) {
			fail = ERROR_BAD_FORMAT;
			goto done;
		}
		if (total > 32u * 1024u * 1024u) {
			fail = ERROR_INSTALL_FAILURE;
			goto done;
		}
		if (!WriteFile(file, chunk, n, &wrote, NULL) || wrote != n) {
			fail = GetLastError();
			goto done;
		}
		total += wrote;
	}
	if (total < 64) {
		fail = ERROR_BAD_FORMAT;
		goto done;
	}
	if (!FlushFileBuffers(file)) {
		fail = GetLastError();
		goto done;
	}
	CloseHandle(file);
	file = INVALID_HANDLE_VALUE;
	if (MsiSetPropertyW(session, L"WinunitdHelperPath", path) != ERROR_SUCCESS)
		goto done;
	result = ERROR_SUCCESS;
done:
	if (file != INVALID_HANDLE_VALUE)
		CloseHandle(file);
	if (result != ERROR_SUCCESS && created)
		DeleteFileW(path);
	if (chunk)
		HeapFree(GetProcessHeap(), 0, chunk);
	if (path)
		HeapFree(GetProcessHeap(), 0, path);
	if (rec)
		MsiCloseHandle(rec);
	if (view) {
		MsiViewClose(view);
		MsiCloseHandle(view);
	}
	if (db)
		MsiCloseHandle(db);
	if (result != ERROR_SUCCESS)
		logNumber(session, L"service helper could not be prepared", fail);
	return result;
}

__declspec(dllexport) UINT __stdcall ExtractServiceHelper(MSIHANDLE session) {
	return extractHelper(session);
}

static int helperFileName(const wchar_t *path) {
	const wchar_t *base = path;
	const wchar_t *prefix = L"winunitd-msi-";
	DWORD i;
	for (; *path; path++) {
		if (*path == L'\\' || *path == L'/')
			base = path + 1;
	}
	for (i = 0; prefix[i]; i++) {
		if (base[i] != prefix[i])
			return 0;
	}
	if (!hexToken(base + i, 32))
		return 0;
	return base[i + 32] == L'.' && base[i + 33] == L'e' && base[i + 34] == L'x' && base[i + 35] == L'e' && base[i + 36] == 0;
}

static int quotedHelperPath(const wchar_t *cmd, wchar_t *dst, unsigned cap) {
	DWORD i = 0;
	DWORD o = 0;
	const wchar_t *tail = L" service-prepare ";
	if (cmd[0] != L'"')
		return 0;
	for (i = 1; cmd[i] && cmd[i] != L'"'; i++) {
		if (o + 1 >= cap)
			return 0;
		dst[o++] = cmd[i];
	}
	if (cmd[i] != L'"' || o == 0)
		return 0;
	dst[o] = 0;
	while (*tail) {
		if (cmd[++i] != *tail++)
			return 0;
	}
	return helperFileName(dst);
}

static int helperFileOk(const wchar_t *path) {
	const wchar_t *p;
	DWORD attrs;
	if (!helperFileName(path))
		return 0;
	for (p = path; *p; p++) {
		if (p[0] == L'.' && p[1] == L'.')
			return 0;
	}
	attrs = GetFileAttributesW(path);
	if (attrs == INVALID_FILE_ATTRIBUTES)
		return 0;
	if (attrs & (FILE_ATTRIBUTE_REPARSE_POINT | FILE_ATTRIBUTE_DIRECTORY))
		return 0;
	return 1;
}

static void logBytes(MSIHANDLE session, const unsigned char *buf, DWORD n) {
	wchar_t *wide = NULL;
	int need;
	DWORD chars;
	DWORD i;
	DWORD start;
	if (!buf || n == 0)
		return;
	need = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, (const char *)buf, (int)n, NULL, 0);
	if (need > 0) {
		wide = HeapAlloc(GetProcessHeap(), 0, ((DWORD)need + 1) * sizeof(wchar_t));
		if (!wide)
			return;
		MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, (const char *)buf, (int)n, wide, need);
		wide[need] = 0;
		chars = (DWORD)need;
	} else {
		wide = HeapAlloc(GetProcessHeap(), 0, (n + 1) * sizeof(wchar_t));
		if (!wide)
			return;
		for (i = 0; i < n; i++)
			wide[i] = buf[i];
		wide[n] = 0;
		chars = n;
	}
	start = 0;
	for (i = 0; i <= chars; i++) {
		if (i == chars || wide[i] == L'\n' || wide[i] == L'\r') {
			wchar_t saved;
			if (i == start) {
				start = i + 1;
				continue;
			}
			saved = wide[i];
			wide[i] = 0;
			logPlain(session, wide + start);
			wide[i] = saved;
			start = i + 1;
		}
	}
	HeapFree(GetProcessHeap(), 0, wide);
}

static void drainPipe(HANDLE pipe, unsigned char *buf, DWORD cap, DWORD *stored) {
	for (;;) {
		unsigned char tmp[512];
		DWORD got = 0;
		DWORD room;
		DWORD copy;
		DWORD i;
		if (!ReadFile(pipe, tmp, sizeof(tmp), &got, NULL) || got == 0)
			return;
		room = cap - *stored;
		copy = got < room ? got : room;
		for (i = 0; i < copy; i++)
			buf[*stored + i] = tmp[i];
		*stored += copy;
	}
}

static UINT runHelper(MSIHANDLE session) {
	wchar_t *cmd = NULL;
	wchar_t *path = NULL;
	DWORD cmdLen = 2048;
	SECURITY_ATTRIBUTES sa;
	HANDLE readPipe = NULL;
	HANDLE writePipe = NULL;
	STARTUPINFOW si;
	PROCESS_INFORMATION pi;
	unsigned char *buf = NULL;
	DWORD stored = 0;
	DWORD start;
	DWORD code = 1;
	UINT result = ERROR_INSTALL_FAILURE;
	BOOL started = FALSE;
	DWORD pathAttrs;

	cmd = HeapAlloc(GetProcessHeap(), HEAP_ZERO_MEMORY, 2048 * sizeof(wchar_t));
	path = HeapAlloc(GetProcessHeap(), HEAP_ZERO_MEMORY, 1024 * sizeof(wchar_t));
	if (!cmd || !path)
		goto done;
	if (MsiGetPropertyW(session, L"CustomActionData", cmd, &cmdLen) != ERROR_SUCCESS || cmdLen == 0 || cmdLen >= 2048)
		goto done;
	if (!quotedHelperPath(cmd, path, 1024) || !helperFileOk(path)) {
		logPlain(session, L"service helper could not be started");
		goto done;
	}
	memzero(&sa, sizeof(sa));
	sa.nLength = sizeof(sa);
	sa.bInheritHandle = TRUE;
	if (!CreatePipe(&readPipe, &writePipe, &sa, 65536))
		goto done;
	SetHandleInformation(readPipe, HANDLE_FLAG_INHERIT, 0);
	memzero(&si, sizeof(si));
	memzero(&pi, sizeof(pi));
	si.cb = sizeof(si);
	si.dwFlags = STARTF_USESTDHANDLES;
	si.hStdOutput = writePipe;
	si.hStdError = writePipe;
	buf = HeapAlloc(GetProcessHeap(), 0, 65536);
	if (!buf)
		goto done;
	if (!CreateProcessW(NULL, cmd, NULL, NULL, TRUE, CREATE_NO_WINDOW, NULL, NULL, &si, &pi)) {
		logNumber(session, L"service helper could not be started", GetLastError());
		goto done;
	}
	started = TRUE;
	CloseHandle(writePipe);
	writePipe = NULL;
	start = GetTickCount();
	for (;;) {
		DWORD avail = 0;
		DWORD wait;
		if (PeekNamedPipe(readPipe, NULL, 0, NULL, &avail, NULL) && avail > 0) {
			unsigned char tmp[512];
			DWORD want = avail > sizeof(tmp) ? (DWORD)sizeof(tmp) : avail;
			DWORD got = 0;
			DWORD room;
			DWORD copy;
			DWORD i;
			if (ReadFile(readPipe, tmp, want, &got, NULL) && got > 0) {
				room = 65536 - stored;
				copy = got < room ? got : room;
				for (i = 0; i < copy; i++)
					buf[stored + i] = tmp[i];
				stored += copy;
			}
		}
		wait = WaitForSingleObject(pi.hProcess, 200);
		if (wait == WAIT_OBJECT_0)
			break;
		if (wait == WAIT_FAILED || GetTickCount() - start > 420000) {
			TerminateProcess(pi.hProcess, 1);
			WaitForSingleObject(pi.hProcess, 5000);
			break;
		}
	}
	drainPipe(readPipe, buf, 65536, &stored);
	logBytes(session, buf, stored);
	if (GetExitCodeProcess(pi.hProcess, &code) && code == 0)
		result = ERROR_SUCCESS;
done:
	if (started) {
		CloseHandle(pi.hThread);
		CloseHandle(pi.hProcess);
	}
	if (writePipe)
		CloseHandle(writePipe);
	if (readPipe)
		CloseHandle(readPipe);
	if (buf)
		HeapFree(GetProcessHeap(), 0, buf);
	if (path && path[0] && helperFileName(path)) {
		pathAttrs = GetFileAttributesW(path);
		if (pathAttrs != INVALID_FILE_ATTRIBUTES && (pathAttrs & FILE_ATTRIBUTE_REPARSE_POINT) == 0)
			DeleteFileW(path);
	}
	if (cmd)
		HeapFree(GetProcessHeap(), 0, cmd);
	if (path)
		HeapFree(GetProcessHeap(), 0, path);
	return result;
}

__declspec(dllexport) UINT __stdcall RunServiceHelper(MSIHANDLE session) {
	return runHelper(session);
}

static int entryIsFail(const wchar_t *entry, DWORD avail) {
	const wchar_t *needle = L"WINUNITD_TEST_FAIL=1";
	DWORD k = 0;
	while (needle[k]) {
		if (k >= avail || entry[k] != needle[k])
			return 0;
		k++;
	}
	return k < avail && entry[k] == 0;
}

static void *readPtr(const unsigned char *p) {
	void *v;
	unsigned char *d = (unsigned char *)&v;
	unsigned i;
	for (i = 0; i < sizeof(v); i++)
		d[i] = p[i];
	return v;
}

static unsigned long readU32(const unsigned char *p) {
	return (unsigned long)p[0] | ((unsigned long)p[1] << 8) | ((unsigned long)p[2] << 16) | ((unsigned long)p[3] << 24);
}

/* Silent repair skips the client sequence on Windows Installer. The elevated
   action then reads WINUNITD_TEST_FAIL from the launching msiexec process.
   x64 offsets: PEB.ProcessParameters at 0x20, Environment at 0x80. */
static int clientHasFail(MSIHANDLE session) {
#if !defined(_WIN64)
	(void)session;
	return 0;
#else
	typedef long(__stdcall *NtQueryInformationProcessFn)(void *, unsigned long, void *, unsigned long, unsigned long *);
	union {
		FARPROC proc;
		NtQueryInformationProcessFn fn;
	} query;
	wchar_t pidText[16];
	DWORD n = 16;
	unsigned long pid = 0;
	DWORD i;
	HANDLE proc = NULL;
	HMODULE ntdll;
	unsigned char peb[0x28];
	unsigned char hdr[0x88];
	SIZE_T got = 0;
	void *pebAddr;
	void *params;
	void *env;
	unsigned long envBytes = 32768;
	wchar_t *block = NULL;
	int found = 0;
	struct {
		void *Reserved1;
		void *PebBaseAddress;
		void *Reserved2[2];
		ULONG_PTR UniqueProcessId;
		void *Reserved3;
	} info;

	memzero(pidText, sizeof(pidText));
	if (MsiGetPropertyW(session, L"CLIENTPROCESSID", pidText, &n) != ERROR_SUCCESS || n == 0 || n >= 16)
		return 0;
	for (i = 0; i < n; i++) {
		if (pidText[i] < L'0' || pidText[i] > L'9')
			return 0;
		pid = pid * 10u + (unsigned long)(pidText[i] - L'0');
	}
	if (pid == 0 || pid == GetCurrentProcessId())
		return 0;
	proc = OpenProcess(PROCESS_QUERY_INFORMATION | PROCESS_VM_READ, FALSE, pid);
	if (!proc)
		proc = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_VM_READ, FALSE, pid);
	if (!proc)
		return 0;
	ntdll = GetModuleHandleW(L"ntdll.dll");
	if (!ntdll)
		ntdll = LoadLibraryW(L"ntdll.dll");
	query.proc = ntdll ? GetProcAddress(ntdll, "NtQueryInformationProcess") : NULL;
	memzero(&info, sizeof(info));
	if (!query.fn || query.fn(proc, 0, &info, sizeof(info), NULL) != 0 || !info.PebBaseAddress)
		goto done;
	if (!ReadProcessMemory(proc, info.PebBaseAddress, peb, sizeof(peb), &got) || got < sizeof(peb))
		goto done;
	pebAddr = readPtr(peb + 0x20);
	params = pebAddr;
	if (!params || !ReadProcessMemory(proc, params, hdr, sizeof(hdr), &got) || got < sizeof(hdr))
		goto done;
	env = readPtr(hdr + 0x80);
	if (readU32(hdr + 4) >= 0x3F4) {
		unsigned char szb[4];
		unsigned long sz;
		if (ReadProcessMemory(proc, (unsigned char *)params + 0x3F0, szb, 4, &got) && got == 4) {
			sz = readU32(szb);
			if (sz >= 64 && sz <= 131072 && (sz % 2) == 0)
				envBytes = sz;
		}
	}
	if (!env)
		goto done;
	block = HeapAlloc(GetProcessHeap(), 0, envBytes);
	if (!block || !ReadProcessMemory(proc, env, block, envBytes, &got) || got < sizeof(wchar_t))
		goto done;
	{
		DWORD chars = (DWORD)(got / sizeof(wchar_t));
		DWORD at = 0;
		while (at < chars && block[at] != 0) {
			DWORD end = at;
			while (end < chars && block[end] != 0)
				end++;
			if (entryIsFail(block + at, end - at + (end < chars ? 1 : 0))) {
				found = 1;
				break;
			}
			if (end >= chars)
				break;
			at = end + 1;
		}
	}
done:
	if (block)
		HeapFree(GetProcessHeap(), 0, block);
	CloseHandle(proc);
	return found;
#endif
}

static int propertyIsOne(MSIHANDLE session) {
	wchar_t existing[8];
	DWORD n = 8;
	memzero(existing, sizeof(existing));
	/* do not clear an already-set secure property */
	if (MsiGetPropertyW(session, L"WINUNITD_TEST_FAIL", existing, &n) != ERROR_SUCCESS)
		return 0;
	return n == 1 && existing[0] == L'1';
}

static int envIsOne(void) {
	wchar_t value[16];
	DWORD n = GetEnvironmentVariableW(L"WINUNITD_TEST_FAIL", value, 16);
	return n == 1 && value[0] == L'1';
}

__declspec(dllexport) UINT __stdcall PullTestFail(MSIHANDLE session) {
	if (propertyIsOne(session))
		return ERROR_SUCCESS;
	if (envIsOne() || clientHasFail(session))
		return MsiSetPropertyW(session, L"WINUNITD_TEST_FAIL", L"1");
	return ERROR_SUCCESS;
}

__declspec(dllexport) UINT __stdcall NewPolicyTransaction(MSIHANDLE session) {
	unsigned char random[16];
	wchar_t token[33];
	const wchar_t hex[] = L"0123456789abcdef";
	unsigned int i;
	if (BCryptGenRandom(NULL, random, sizeof(random), BCRYPT_USE_SYSTEM_PREFERRED_RNG) != 0)
		return ERROR_INSTALL_FAILURE;
	for (i = 0; i < sizeof(random); ++i) {
		token[2 * i] = hex[random[i] >> 4];
		token[2 * i + 1] = hex[random[i] & 15];
	}
	token[32] = 0;
	return MsiSetPropertyW(session, L"WinunitdPolicyTransaction", token);
}
