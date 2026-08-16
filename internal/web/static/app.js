// Ziba's only JavaScript.
//
// Everything here is progressive enhancement: each feature improves something
// that already works without it. With this file blocked or failing to load, the
// date picker still has its button and marking read still posts and redirects.
// Nothing below is required to use the site.

(function () {
  'use strict';

  // Choosing a date navigates, instead of asking for a second click on "Show".
  var datePicker = document.querySelector('.daypick input[type=date]');
  if (datePicker) {
    datePicker.addEventListener('change', function () {
      this.form.submit();
    });
  }

  // Marking read happens in place.
  //
  // The plain form posts and redirects back, which reloads the page and returns
  // the reader to the top of it — so marking the fourth article read means
  // scrolling back down to reach the fifth. Here the post is sent in the
  // background and the card is updated where it sits.
  //
  // Listening on the document rather than on each form: one listener, and it
  // keeps working for anything added to the page later.
  document.addEventListener('submit', function (event) {
    var form = event.target;
    if (!form.classList.contains('inline')) {
      return;
    }
    event.preventDefault();

    var button = form.querySelector('button');
    button.disabled = true;

    fetch(form.action, {
      method: 'POST',
      body: new FormData(form),
      // Tells the handler to answer with a bare 204 rather than the redirect a
      // browser form needs.
      headers: { 'X-Ziba-Async': '1' },
      credentials: 'same-origin'
    }).then(function (response) {
      if (!response.ok) {
        throw new Error('unexpected status ' + response.status);
      }
      button.disabled = false;
      // Every control for this article, not just the one clicked: the reader
      // shows the same button at the head and the foot of the page, and one
      // flipping while the other did not would be worse than a reload.
      var article = articleOf(form);
      document.querySelectorAll('form.inline').forEach(function (other) {
        if (articleOf(other) === article) {
          toggle(other, other.querySelector('button'));
        }
      });
    }).catch(function () {
      // Fall back to the ordinary post rather than leaving the reader unsure
      // whether the click registered. Worst case they get the old behaviour.
      form.submit();
    });
  });

  // articleOf reads the article's id out of a form's address, which is either
  // /article/{id}/archive or /article/{id}/unarchive.
  function articleOf(form) {
    var parts = form.getAttribute('action').split('/');
    return parts[2];
  }

  // toggle swaps the control to its opposite state.
  //
  // Both labels come from the template, carried on the button as data
  // attributes, so the wording lives in one place and this file does not have
  // to keep a copy of it in step.
  function toggle(form, button) {
    var label = button.textContent;
    var title = button.title;
    button.textContent = button.dataset.altLabel;
    button.title = button.dataset.altTitle;
    button.dataset.altLabel = label;
    button.dataset.altTitle = title;

    var read = form.getAttribute('action').indexOf('/unarchive') === -1;
    form.setAttribute('action',
      form.getAttribute('action').replace(/\/(un)?archive$/, read ? '/unarchive' : '/archive'));

    // Dim the card so a glance down the page shows what has been dealt with.
    // The reader view has no card, and needs none: the button says it.
    var card = form.closest('.card');
    if (card) {
      card.classList.toggle('read', read);
    }
  }

  // Interest rows can be added and removed without a frontend framework. The
  // server still validates every submitted value.
  document.addEventListener('click', function (event) {
    if (event.target.matches('[data-add-interest]')) {
      var template = document.querySelector('[data-interest-template]');
      var list = document.querySelector('[data-interest-list]');
      if (template && list) {
        list.appendChild(template.content.cloneNode(true));
      }
    }
    if (event.target.matches('[data-remove-interest]')) {
      var rows = document.querySelectorAll('.interest-row');
      if (rows.length > 1) {
        event.target.closest('.interest-row').remove();
      }
    }
  });
})();
