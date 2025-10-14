package main

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

// TODO ignore opts?
// --log-level, -q, --quiet, --read-args-from-stdin, --dump-default-toc-xsl
// --dump-outline <file>, --allow <path>, --cache-dir <path>,
// --disable-local-file-access, --enable-local-file-access

// TODO sensitive opts to be hidden from log
// --cookie <name> <value>, --password <password>,
// --ssl-key-password <password>

func wkhtmltopdfBin() string {
	bin := os.Getenv("KWKHTMLTOPDF_BIN")
	if bin != "" {
		return bin
	}
	return "wkhtmltopdf"
}

func isDocOption(arg string) bool {
	switch arg {
	case
		"-h",
		"--help",
		"-H",
		"--extended-help",
		"-V",
		"--version",
		"--readme",
		"--license",
		"--htmldoc",
		"--manpage":
		return true
	}
	return false
}

func httpError(w http.ResponseWriter, err error, code int) {
	log.Println(err)
	http.Error(w, err.Error(), code)
}

func httpAbort(w http.ResponseWriter, err error) {
	log.Println(err)
	// abort chunked encoding response as crude way to report error to client
	wh, ok := w.(http.Hijacker)
	if !ok {
		log.Println("cannot abort connection, error not reported to client: http.Hijacker not supported")
		return
	}
	c, _, err := wh.Hijack()
	if err != nil {
		log.Println("cannot abort connection, error not reported to client: ", err)
		return
	}
	c.Close()
}

func redactArgs(args []string) []string {
	redacted := make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		if args[i] == "--cookie" && i+2 < len(args) {
			redacted = append(redacted, args[i], args[i+1], "***")
			i += 3
		} else {
			redacted = append(redacted, args[i])
			i++
		}
	}
	return redacted
}

func handler(w http.ResponseWriter, r *http.Request) {

	if r.URL.Path == "/status" {
		w.WriteHeader(http.StatusOK)
		return
	} else {
		// don't log status
		log.Printf("%s %s", r.Method, r.URL.Path)
	}
	if r.Method != http.MethodPost {
		httpError(w, errors.New("http method not allowed: "+r.Method), http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/" && r.URL.Path != "/pdf" {
		// handle / and /pdf, keep the rest for future use
		httpError(w, errors.New("path not found: "+r.URL.Path), http.StatusNotFound)
		return
	}

	// temp dir for files
	tmpdir, err := ioutil.TempDir("", "kwk")
	if err != nil {
		httpError(w, err, http.StatusNotFound)
		return
	}
	defer os.RemoveAll(tmpdir)

	// parse request
	reader, err := r.MultipartReader()
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	var docOutput bool
	var args []string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		if part.FormName() == "option" {
			buf := new(bytes.Buffer)
			buf.ReadFrom(part)
			arg := buf.String()
			args = append(args, arg)
			if isDocOption(arg) {
				docOutput = true
			}
		} else if part.FormName() == "file" {
			// It's important to preserve as much as possible of the
			// original filename because some javascript can depend on it
			// through document.location.
			path := filepath.Join(tmpdir, filepath.Base(part.FileName()))
			// TODO what if multiple files with same basename?
			file, err := os.Create(path)
			if err != nil {
				httpError(w, err, http.StatusBadRequest)
				return
			}
			_, err = io.Copy(file, part)
			file.Close()
			if err != nil {
				httpError(w, err, http.StatusBadRequest)
				return
			}
			args = append(args, path)
		} else {
			httpError(w, errors.New("unpexpected part name: "+part.FormName()), http.StatusBadRequest)
			return
		}
	}

	if docOutput {
		w.Header().Add("Content-Type", "text/plain")
	} else {
		w.Header().Add("Content-Type", "application/pdf")
		args = append(args, "-")
	}

	var redactedArgs = redactArgs(args)

	log.Println(redactedArgs, "starting")

	cmd := exec.Command(wkhtmltopdfBin(), args...)
	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	cmd.Stderr = os.Stderr
	err = cmd.Start()
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, err = io.Copy(w, cmdStdout)
	if err != nil {
		httpAbort(w, err)
		return
	}
	err = cmd.Wait()
	if err != nil {
		httpAbort(w, err)
		return
	}

	log.Println(redactedArgs, "success")
}

func main() {
	http.HandleFunc("/", handler)
	log.Println("kwkhtmltopdf server listening on port 8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
