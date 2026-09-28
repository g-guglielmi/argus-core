// Apply the saved theme before paint to avoid a flash of the wrong theme. A separate file rather
// than an inline script so the Content-Security-Policy can refuse inline scripts altogether.
try {
  var t = localStorage.getItem('argus-theme');
  if (t === 'dark' || t === 'light') document.documentElement.setAttribute('data-theme', t);
} catch (e) {}
