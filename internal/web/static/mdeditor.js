// Markdown editor shared by the story and bio editors: toolbar, image
// upload (POST /write/upload) and preview (POST /write/preview).
// Markup: a [data-md-editor] wrapper holding textarea[data-md-body],
// [data-md-preview] (hidden), button[data-md-toggle], button[data-wrap],
// button[data-line], button[data-md-link] and input[type=file][data-md-img].
(function () {
  function upload(file, done) {
    var fd = new FormData();
    fd.append('file', file);
    fetch('/write/upload', { method: 'POST', body: fd }).then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t); });
      return r.json();
    }).then(function (j) { done(j.url); }).catch(function (err) { alert('Upload failed: ' + err.message); });
  }
  window.dsnUpload = upload;

  document.querySelectorAll('[data-md-editor]').forEach(function (root) {
    var body = root.querySelector('[data-md-body]');
    function insert(before, after) {
      var s = body.selectionStart, e = body.selectionEnd, v = body.value;
      body.value = v.slice(0, s) + before + v.slice(s, e) + (after || '') + v.slice(e);
      body.focus();
      body.selectionStart = s + before.length;
      body.selectionEnd = e + before.length;
    }
    root.querySelectorAll('[data-wrap]').forEach(function (b) {
      b.addEventListener('click', function () { insert(b.dataset.wrap, b.dataset.wrap); });
    });
    root.querySelectorAll('[data-line]').forEach(function (b) {
      b.addEventListener('click', function () {
        var s = body.selectionStart, v = body.value, start = v.lastIndexOf('\n', s - 1) + 1;
        body.value = v.slice(0, start) + b.dataset.line + v.slice(start);
        body.focus();
      });
    });
    root.querySelectorAll('[data-md-link]').forEach(function (b) {
      b.addEventListener('click', function () {
        var s = body.selectionStart, e = body.selectionEnd;
        var text = body.value.slice(s, e) || 'link text';
        body.value = body.value.slice(0, s) + '[' + text + '](/)' + body.value.slice(e);
        body.focus();
        body.selectionStart = body.selectionEnd = s + text.length + 3;
      });
    });
    root.querySelectorAll('[data-md-img]').forEach(function (inp) {
      inp.addEventListener('change', function (ev) {
        var f = ev.target.files[0];
        if (f) upload(f, function (url) { insert('\n![](' + url + ')\n'); });
        ev.target.value = '';
      });
    });
    var pv = root.querySelector('[data-md-preview]'), pb = root.querySelector('[data-md-toggle]');
    if (pv && pb) {
      pb.addEventListener('click', function () {
        if (!pv.hidden) { pv.hidden = true; body.hidden = false; pb.textContent = 'Preview'; return; }
        var fd = new URLSearchParams();
        fd.append('body', body.value);
        fetch('/write/preview', { method: 'POST', body: fd }).then(function (r) { return r.text(); }).then(function (html) {
          pv.innerHTML = html; pv.hidden = false; body.hidden = true; pb.textContent = 'Edit';
        });
      });
    }
  });
})();
