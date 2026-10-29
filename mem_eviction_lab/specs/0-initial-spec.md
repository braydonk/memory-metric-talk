# Memory Eviction Lab

This project is a lab to demonstrate the Linux kernel's eviction behaviour specifically on a typical OpenTelemetry Collector. It will use OpenTelemetry Collector Contrib downloaded from the GitHub releases. It will run the Collector under a cgroup with a specified memory limit to trigger memory pressure behaviour early.

Collector Contrib has a very large RSS value on startup on account of all the file-backed pages that come in from the binary being loaded. Putting a low limit on the cgroup, the hypothesis is that much of the memory can be safely reclaimed and not page-thrashed back in when using a smaller configuration because of how little code the Collector will actually need to run under a normal configuration; lots of data is paged in from components that won't ever be used.

The lab should take a yaml configuration for each run:
* Name of the run
* Specify a Collector binary to use
    - Should be possible to specify a binary using a download URL or a path
* Specify the memory limit to use for the cgroup process
* Path to a collector config to use

The `manager` program that manages the lab is a Go program that:

* Takes a configuration, duration, and results directory
* Root is required for the program because it needs to read `/proc/[pid]/kpagemaps` and make a cgroup
* Reads the yaml configuration to know what to do for this run
* Create the cgroup with the configured memory limit 
* It will start the Collector with the requested config file
* The Collector process will be put into the created cgroup
* At Collector process start, read `/proc/[pid]/pagemap` and `/proc/[pid]/kpagemaps` to find all the file-backed pages that came from the Collector binary
* Create an `epoll` for `/sys/fs/cgroup/.../memory.events` to determine when reclaim occurs
* When reclaim occurs, figure out which pages were evicted/paged back in since last read
* Keep track of the results each poll so that they can be output in the results directory
* The resulting data should be written to a CSV for a second visualizer program to create a web dashboard out of

The `visualizer` program will:

* Read a result directory to be able to chart page eviction and thrashing information that was recorded

In the end, it should be easy to tell from the visualized results whether a Collector process was able to successfully evict unused file-backed pages at the given memory limit without incurring page thrashing penalties.

