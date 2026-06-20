# Tool Fluency Experiment: observer boundary

Let the observer own the callback work. The caller's role is to start the
observer, create the watch, trigger the watched event, and use the observer's
callback as the result signal.
