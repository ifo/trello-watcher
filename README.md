# trello watcher

A very opinionated helper system using the trello api via trel.

It requires a board that looks like this:

| Projects  | Active | To Do | Done  | Storage    |
| --------- | ------ | ----- | ----- | ---------- |
| Project 1 | Work   | Do it | Check | Everything |

It will keep track of checklists on active projects and ensure they are mapped to cards on the To Do and Done lists.

Storage contains currently unused cards, so they don't have to be archived.
Any other lists that exist will be ignored, in addition to their positioning.