(function () {
  var t = null;
  try { t = localStorage.getItem('vlan.theme'); } catch (e) {}
  if (t !== 'light' && t !== 'dark') t = window.matchMedia && matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  document.documentElement.dataset.theme = t;
  document.documentElement.style.colorScheme = t;
  try {
    var l = localStorage.getItem('lang');
    if (l === 'ru' || l === 'en') document.documentElement.lang = l;
  } catch (e) {}
})();
