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

  document.addEventListener('change', function (event) {
    if (event.target.matches('[data-source-type]')) {
      toggleSourceFields(event.target.closest('[data-source-card]'));
    }
  });

  document.querySelectorAll('[data-source-card]').forEach(toggleSourceFields);

  // Collection is global state, shown above the interest tabs. Polling keeps
  // that small status current; only an empty home page reloads after a run, so
  // reading and forms are never interrupted.
  var collectionStatus = document.querySelector('[data-collection-status]');
  if (collectionStatus) {
    var wasRunning = collectionStatus.dataset.running === 'true';
    var completed = Number(collectionStatus.dataset.completed || 0);
    var pollTimer;

    function scheduleStatusPoll(delay) {
      clearTimeout(pollTimer);
      pollTimer = setTimeout(pollCollectionStatus, delay);
    }

    function pollCollectionStatus() {
      if (document.hidden) return;
      fetch('/status', { credentials: 'same-origin' }).then(function (response) {
        if (!response.ok) throw new Error('unexpected status ' + response.status);
        return response.json();
      }).then(function (status) {
        var text = status.running ? 'Collecting and preparing your digest…' :
          (status.disabled ? 'Automatic collection is off.' : status.next_label);
        var textNode = collectionStatus.querySelector('[data-collection-status-text]');
        if (textNode.textContent !== text) textNode.textContent = text;
        collectionStatus.querySelector('.collection-dot').classList.toggle('running', status.running);

        var finished = (wasRunning && !status.running) || status.completed > completed;
        wasRunning = status.running;
        completed = status.completed;
        if (finished && document.querySelector('[data-empty-digest]')) {
          window.location.reload();
          return;
        }
        scheduleStatusPoll(status.running ? 3000 : 10000);
      }).catch(function () {
        scheduleStatusPoll(30000);
      });
    }

    document.addEventListener('visibilitychange', function () {
      if (!document.hidden) pollCollectionStatus();
      else clearTimeout(pollTimer);
    });
    scheduleStatusPoll(wasRunning ? 3000 : 10000);
  }

  function toggleSourceFields(card) {
    if (!card) return;
    var type = card.querySelector('[data-source-type]').value;
    card.querySelectorAll('[data-fields]').forEach(function (fields) {
      var hidden = fields.dataset.fields !== type;
      fields.hidden = hidden;
      fields.querySelectorAll('input, select, textarea').forEach(function (field) {
        field.disabled = hidden;
      });
    });
  }

})();
