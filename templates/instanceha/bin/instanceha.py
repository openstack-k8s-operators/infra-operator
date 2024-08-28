#!/usr/libexec/platform-python -tt

import os
import sys
import time
import atexit
import logging
import inspect
from datetime import datetime, timedelta
import concurrent.futures
import requests.exceptions
import yaml
import subprocess
import random
from yaml.loader import SafeLoader
import socket
import struct
import json

from novaclient import client
from novaclient.exceptions import Conflict, NotFound, Forbidden, Unauthorized

from keystoneauth1 import loading
from keystoneauth1 import session as ksc_session
from keystoneauth1.exceptions.discovery import DiscoveryFailure
from keystoneauth1.exceptions.http import Unauthorized

TRUE_TAGS = 'true'

UDP_IP = ''
UDP_PORT =  os.getenv('UDP_PORT', 7410)

with open("/var/lib/instanceha/config.yaml", 'r') as stream:
    try:
        config = yaml.load(stream, Loader=SafeLoader)["config"]
    except yaml.YAMLError as exc:
        logging.warning("Could not load configuration from config file, using defaults")
        logging.debug(exc)

EVACUABLE_TAG = config["EVACUABLE_TAG"] if 'EVACUABLE_TAG' in config else "evacuable"
DELTA = int(config["DELTA"]) if 'DELTA' in config else 30
POLL = int(config["POLL"]) if 'POLL' in config else 30
THRESHOLD = int(config["THRESHOLD"]) if 'THRESHOLD' in config else 50
WORKERS = int(config["WORKERS"]) if 'WORKERS' in config else 4
SMART_EVACUATION = config["SMART_EVACUATION"] if 'SMART_EVACUATION' in config else "false"
RESERVED_HOSTS = config["RESERVED_HOSTS"] if 'RESERVED_HOSTS' in config else "false"
LEAVE_DISABLED = config["LEAVE_DISABLED"] if 'LEAVE_DISABLED' in config else "false"
CHECK_KDUMP = config["CHECK_KDUMP"] if 'CHECK_KDUMP' in config else "false"
LOGLEVEL = config["LOGLEVEL"].upper() if 'LOGLEVEL' in config else "INFO"
DISABLED = config["DISABLED"] if 'DISABLED' in config else "false"

logging.basicConfig(format='%(asctime)s %(levelname)s %(message)s', level=LOGLEVEL)


with open("/secrets/fencing.yaml", 'r') as stream:
    try:
        fencing = yaml.load(stream, Loader=SafeLoader)["FencingConfig"]
    except yaml.YAMLError as exc:
        logging.error('Could not load fencing secret')
        logging.error(exc)

with open("/home/cloud-admin/.config/openstack/clouds.yaml", 'r') as stream:
    try:
        clouds = yaml.load(stream, Loader=SafeLoader)["clouds"]
    except yaml.YAMLError as exc:
        logging.error('Could not read from /clouds.yaml')
        logging.debug(exc)

with open("/home/cloud-admin/.config/openstack/secure.yaml", 'r') as stream:
    try:
        secure = yaml.load(stream, Loader=SafeLoader)["clouds"]
    except yaml.YAMLError as exc:
        logging.error('Could not read from secure.yaml')
        logging.debug(exc)


def _is_server_evacuable(server, evac_flavors, evac_images):

    if evac_images and hasattr(server.image, 'get') and server.image.get('id') in evac_images:
        return True
    if evac_flavors and server.flavor.get('extra_specs').get(EVACUABLE_TAG) and 'true' in server.flavor.get('extra_specs').get(EVACUABLE_TAG):
        return True

    logging.warning("Instance %s is not evacuable: not using either of the defined flavors or images tagged with the %s attribute" % (server.id, EVACUABLE_TAG))
    return False

# returns all the flavors where EVACUABLE_TAG has been set
def _get_evacuable_flavors(connection):

    result = []
    flavors = connection.flavors.list(is_public=None)
    result = [flavor.id for flavor in flavors if EVACUABLE_TAG in flavor.get_keys() and TRUE_TAGS in flavor.get_keys().get(EVACUABLE_TAG)]

    return result


# returns all the images where evacuable_tag metadata has been set
def _get_evacuable_images(connection):

    result = []
    images = []
    if hasattr(connection, "glance"):
        images = connection.glance.list()

    result = [image.id for image in images if hasattr(image, 'tags') and EVACUABLE_TAG in image.tags]

    return result


# check if a host is part of an aggregate that has been tagged with the evacuable_tag
def _is_aggregate_evacuable(connection, host):

    aggregates = connection.aggregates.list()
    evacuable_aggregates = [i for i in aggregates if EVACUABLE_TAG in i.metadata]
    result = any(host in i.hosts for i in evacuable_aggregates)
    if not result:
        logging.warning("Host %s is not part of an aggregate tagged with %s. It will not be evacuated" %(host, EVACUABLE_TAG))

    return result


def _custom_check():
    logging.info("Ran _custom_check()")
    return result


def _host_evacuate(connection, host):
    result = True
    images = _get_evacuable_images(connection)
    flavors = _get_evacuable_flavors(connection)
    servers = connection.servers.list(search_opts={'host': host, 'all_tenants': 1 })
    servers = [server for server in servers if server.status in {'ACTIVE', 'ERROR', 'STOPPED'}]

    if flavors or images:
        logging.debug("Filtering images and flavors: %s %s" % (repr(flavors), repr(images)))
        # Identify all evacuable servers
        logging.debug("Checking %s" % repr(servers))
        evacuables = [server for server in servers if _is_server_evacuable(server, flavors, images)]
        logging.debug("Evacuating %s" % repr(evacuables))
    else:
        logging.debug("Evacuating all images and flavors")
        evacuables = servers

    if evacuables == []:
        logging.info("Nothing to evacuate")
        return True

    # if SMART_EVACUATION is 'True' (string) use a ThreadPoolExecutor to poll the evacuation status
    # otherwise use the old "fire and forget" approach
    if 'true' in SMART_EVACUATION.lower():

        # at this point we have a list of servers to evacuate called 'evacuables'
        # we spawn X (4) threads to evacuate X vms in parallel and follow the evacuation

        with concurrent.futures.ThreadPoolExecutor(max_workers=WORKERS) as executor:
            future_to_server = {executor.submit(_server_evacuate_future, connection, server): server for server in evacuables}

            for future in concurrent.futures.as_completed(future_to_server):
                server = future_to_server[future]
                try:
                    data = future.result()
                    if data is False:
                        logging.debug('Evacuation of %s failed 5 times in a row' % server.id)
                        return data
                except Exception as exc:
                    logging.error('Evacuation generated an exception: %s' % exc)
                else:
                    logging.info('%r evacuated successfully' % server.id)
        #return result

    else:
        for server in evacuables:
            logging.debug("Processing %s" % server)
            if hasattr(server, 'id'):
                response = _server_evacuate(connection, server.id)
                if response["accepted"]:
                    logging.debug("Evacuated %s from %s: %s" % (response["uuid"], host, response["reason"]))
                else:
                    logging.warning("Evacuation of %s on %s failed: %s" % (response["uuid"], host, response["reason"]))
                    result = False
            else:
                logging.error("Could not evacuate instance: %s" % server.to_dict())
                # Should a malformed instance result in a failed evacuation?
                # result = False
        return result

    return result


def _server_evacuate(connection, server):
    try:
        logging.debug("Resurrecting instance: %s" % server)
        response, dictionary = connection.servers.evacuate(server=server)
        if response is None:
            error_message = "No response received while evacuating instance"
        elif response.status_code == 200:
            success = True
            error_message = response.reason
        else:
            error_message = response.reason
    except NotFound:
        error_message = "Instance %s not found" % server
    except Forbidden:
        error_message = "Access denied while evacuating instance %s" % server
    except Unauthorized:
        error_message = "Authentication failed while evacuating instance %s" % server
    except Exception as e:
        error_message = "Error while evacuating instance %s: %s" % (server, e)

    return {
        "uuid": server,
        "accepted": success,
        "reason": error_message,
    }


def _server_evacuation_status(connection, server):
    completed = False
    error_msg = False

    try:
        # query nova for the instance's migrations (limit to last hour).
        # use this info to poll the evacuation status.
        # nova api v2.59 lists them in reverse time order (last first) so we need to check the first item "[0]"

        logging.debug("Polling evacuation of instance: %s" % server)
        #FIXME: this not really lasthour, more last5minutes
        lasthour = (datetime.now() - timedelta(minutes=5)).isoformat()

        try:
            last_migration = connection.migrations.list(instance_uuid=server,
                                                        migration_type='evacuation',
                                                        changes_since=lasthour,
                                                        limit='100000')[0]
        except IndexError:
            logging.error("No evacuations found for instance: %s" % server)
            error_msg = True
            return {
                "completed": completed,
                "error": error_msg,
            }

        try:
            status = last_migration._info['status']
        except (AttributeError, KeyError):
            logging.error("Failed to get evacuation status for instance: %s" % server)
            error_msg = True
            return {
                "completed": completed,
                "error": error_msg,
            }

        logging.debug("%s evacuation status: %s" % (server, status))

        if status in ['completed', 'done']:
            completed = True
            error_msg = False
        elif status == 'migrating':
            completed = False
            error_msg = False
        elif status == 'error':
            completed = False
            error_msg = True

    except Exception as e:
        logging.error("Error while evacuating instance: %s" % e)
        error_msg = True

    return {
        "completed": completed,
        "error": error_msg,
    }


def _server_evacuate_future(connection, server):

    try:
        error_count
    except:
        error_count = 0

    logging.info("Processing %s" % server.id)
    if hasattr(server, 'id'):

        response = _server_evacuate(connection, server.id)

        if response["accepted"]:
            logging.debug("Starting evacuation of %s" % response["uuid"])

            time.sleep(10)

            while True:

                status = _server_evacuation_status(connection, server.id)

                if status["completed"]:
                    logging.info("Evacuation of %s completed" % response["uuid"])
                    result = True
                    break
                if status["error"]:
                    if error_count == 5:
                        logging.error("Failed evacuating %s 5 times. Giving up." % response["uuid"])
                        result = False
                        break
                    try:
                        error_count
                    except:
                        error_count = 0
                    error_count += 1
                    logging.warning("Evacuation of instance %s failed %s times. Trying again." % (response["uuid"], error_count))
                    time.sleep(5)
                    continue
                #evacuation not finished, poll again.
                logging.debug("Evacuation of %s still in progress" % response["uuid"])
                time.sleep(5)
                continue
        else:
            logging.warning("Evacuation of %s on %s failed: %s" %
                            (response["uuid"], server.id, response["reason"]))
            result = False
    else:
        logging.warning("Could not evacuate instance: %s" % server.to_dict())
        # Should a malformed instance result in a failed evacuation?
        # result = False
    return result


def nova_login(username, password, projectname, auth_url, user_domain_name, project_domain_name):
    try:
        loader = loading.get_plugin_loader("password")
        auth = loader.load_from_options(
            auth_url=auth_url,
            username=username,
            password=password,
            project_name=projectname,
            user_domain_name=user_domain_name,
            project_domain_name=project_domain_name,
        )

        session = ksc_session.Session(auth=auth)
        nova = client.Client("2.59", session=session)
        nova.versions.get_current()
    except DiscoveryFailure as e:
        logging.error("Nova login failed: Discovery Failure: %s" % e)
        return None
    except Unauthorized as e:
        logging.error("Nova login failed: Unauthorized: %s" % e)
        return None
    except Exception as e:
        logging.error("Nova login failed: %s" % e)
        logging.debug('Exception traceback:', exc_info=True)
        return None

    logging.info("Nova login successful")
    return nova


def _host_disable(connection, service):
    try:
        logging.info('Forcing %s down before evacuation' % service.host)
        connection.services.force_down(service.id, True)
    except NotFound as e:
        logging.error('Failed to force-down service %s on host %s. Resource not found: %s', service.binary, service.host, e)
        logging.debug('Exception traceback:', exc_info=True)
        return False
    except Conflict as e:
        logging.error('Failed to force-down service %s on host %s. Conflicting operation: %s', service.binary, service.host, e)
        logging.debug('Exception traceback:', exc_info=True)
        return False
    except Exception as e:
        logging.error('Failed to force-down service %s on host %s. Error: %s', service.binary, service.host, e)
        logging.debug('Exception traceback:', exc_info=True)
        return False
    finally:
        try:
            connection.services.disable_log_reason(service.id, "instanceha evacuation: %s" % datetime.now().isoformat())
            logging.info('Service %s on host %s is now disabled', service.binary, service.host)
            return True
        except Exception as e:
            logging.error('Failed to log reason for disabling service %s on host %s. Error: %s', service.binary, service.host, e)
            logging.debug('Exception traceback:', exc_info=True)
            return False


def _check_kdump(stale_services):
    FENCE_KDUMP_MAGIC = "0x1B302A40"

    TIMEOUT = 30

    sock = socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
    sock.settimeout(30)

    try:
        sock.bind((UDP_IP, UDP_PORT))
    except:
        logging.error('Could not bind to %s on port %s' % (UDP_IP,UDP_PORT))
        return 1

    t_end = time.time() + TIMEOUT

    broken_computes = set()
    for service in stale_services:
        broken_computes.add(service.host)

    # we use a set since it doesn't allow duplicates
    dumping = set()

    while time.time() < t_end:

        try:
            data, ancdata, msg_flags, address = sock.recvmsg(65535, 1024, 0)
        except OSError as msg:
            logging.info('No kdump msg received in 30 seconds')
            sock.close()
            sock = None
            continue

        #logging.debug("received message: %s %s %s %s" % (hex(struct.unpack('ii',data)[0]), ancdata, msg_flags, address))
        #logging.debug("address is %s" % address[0])

        # short hostname
        name = socket.gethostbyaddr(address[0])[0].split('.', 1)[0]

        # fence_kdump checks if the magic number matches, so let's do it here too
        if hex(struct.unpack('ii',data)[0]).upper() != FENCE_KDUMP_MAGIC.upper() :
            logging.debug("invalid magic number - did not match %s" % hex(struct.unpack('ii',data)[0]).upper())
            continue

        # this prints the msg version (0x1) so not sure if we need this at all
        # logging.debug(hex(struct.unpack('ii',data)[1]))

        # let's check if the source host is valid / known address is a tuple ('ip', 'port')
        name = [i for i in broken_computes if name in i]
        if name:
            logging.debug('host %s matched' % name)
            dumping.add(name[0])
        else:
            logging.debug('received message from unknown host')

    broken_computes = broken_computes.difference(dumping)
    logging.info('the following compute(s) are kdumping: %s' % dumping)
    logging.info('the following compute(s) are not kdumping: %s' % broken_computes)

    if broken_computes:
        stale_services = [service for service in stale_services if service.host in broken_computes]

    return stale_services


def _host_enable(connection, service, reenable=False):
    if reenable:
        try:
            logging.info('Unsetting force-down on host %s after evacuation', service.host)
            connection.services.force_down(service.id, False)
            logging.info('Successfully unset force-down on host %s', service.host)
        except:
            logging.error('Could not unset force-down for %s. Please check the host status and perform manual cleanup if necessary', service.host)
            return False

    else:
        for _ in range(3):
            try:
                logging.info('Trying to enable %s', service.host)
                connection.services.enable(service.id)
                logging.info('Host %s is now enabled', service.host)
            except Exception as e:
                err = e
                continue
            else:
                break
        else:
            raise err
            return False

    return True


def _redfish_reset(url, user, passwd, timeout, action):

    payload = {"ResetType": action}
    headers = {'content-type': 'application/json'}
    url += '/Actions/ComputerSystem.Reset'

    r = requests.post(url, data=json.dumps(payload), headers=headers, auth=(user, passwd), verify=False, timeout=timeout)
    return r


def _host_fence(host, action):
    logging.info('Fencing host %s %s' % (host, action))

    try:
        # Collecting fencing data for the given host
        fencing_data = [value for key, value in fencing.items() if key in host.split('.', 1)[0]][0]

    except:
        logging.error('No fencing data found for %s' % host)
        return False

    elif 'noop' in fencing_data["agent"]:
        logging.warning('Using noop fencing agent. VMs may get corrupted.')
        return True

    if 'ipmi' in fencing_data["agent"]:

        # Extracting the necessary parameters from the fencing data
        ip = str(fencing_data["ipaddr"])
        port = str(fencing_data["ipport"])
        user = str(fencing_data["login"])
        passwd = str(fencing_data["passwd"])

        logging.debug('Checking %s power status' % host)

        cmd = ['ipmitool', '-I', 'lanplus', '-H', ip, '-U', user, '-P', passwd, '-p', port, 'power', 'status']

        if action == 'off':
            cmd = ["ipmitool", "-I", "lanplus", "-H", "%s" % ip, "-U", "%s" % user, "-P", "%s" % passwd, "-p", "%s" % port, "power", "off"]
            try:
                cmd_output = subprocess.run(cmd, timeout=30, capture_output=True, text=True, check=True)
            except subprocess.TimeoutExpired:
                logging.error('Timeout expired while sending IPMI command for power off of %s' % host)
                return False
            except subprocess.CalledProcessError as e:
                logging.error('Error while sending IPMI command for power off of %s: %s' % (host, e))
                return False

            logging.info('Successfully powered off %s' % host)
            return True
        else:
            cmd = ["ipmitool", "-I", "lanplus", "-H", "%s" % ip, "-U", "%s" % user, "-P", "%s" % passwd, "-p", "%s" % port, "power", "on"]
            try:
                cmd_output = subprocess.run(cmd, timeout=30, capture_output=True, text=True, check=True)
            except subprocess.TimeoutExpired:
                logging.error('Timeout expired while sending IPMI command for power on of %s' % host)
                return False
            except subprocess.CalledProcessError as e:
                logging.error('Error while sending IPMI command for power on of %s: %s' % (host, e))
                return False

            logging.info('Successfully powered on %s' % host)
            return True


    elif 'redfish' in fencing_data["agent"]:

        ip = str(fencing_data["ipaddr"])
        port = str(fencing_data["ipport"]) if "ipport" in fencing_data else "443"
        user = str(fencing_data["login"])
        passwd = str(fencing_data["passwd"])
        uuid = str(fencing_data["uuid"]) if "uuid" in fencing_data else "System.Embedded.1"
        timeout = fencing_data["timeout"] if "timeout" in fencing_data else 30

        tls = str(fencing_data["tls"]) if "tls" in fencing_data else "false"

        logging.debug('Checking %s power status' % host)

        if tls == "true":
            url = 'https://%s:%s/redfish/v1/Systems/%s' %  (ip, port, uuid)
        else:
            url = 'http://%s:%s/redfish/v1/Systems/%s' %  (ip, port, uuid)

        if action == 'off':
            r = _redfish_reset(url, user, passwd, timeout, "ForceOff")

            if r.status_code == 204:
                logging.info('Power off of %s ok' % host)
                return True
            else:
                logging.error('Could not power off %s' % host)
                return False
        else:
            r = _redfish_reset(url, user, passwd, timeout, "On")
            if r.status_code == 204:
                logging.info('Power on of %s ok' % host)
            else:
                logging.warning('Could not power on %s' % host)
                #return True

    else:
        logging.error('No valid fencing method detected for %s' % host)
        return False

    return True


def process_service(service, reserved_hosts):

    try:
        logging.info('Fencing %s' % service.host)
        _host_fence(service.host, 'off')
    except Exception as e:
        logging.error('Failed to fence %s: %s' % (service.host, e))
        return False

    CLOUD = os.getenv('OS_CLOUD', 'overcloud')

    username = clouds[CLOUD]["auth"]["username"]
    projectname = clouds[CLOUD]["auth"]["project_name"]
    auth_url = clouds[CLOUD]["auth"]["auth_url"]
    user_domain_name = clouds[CLOUD]["auth"]["user_domain_name"]
    project_domain_name = clouds[CLOUD]["auth"]["project_domain_name"]
    password = secure[CLOUD]["auth"]["password"]

    try:
        conn = nova_login(username, password, projectname, auth_url, user_domain_name, project_domain_name)

    except Exception as e:
        logging.error("Failed: Unable to connect to Nova: " + str(e))

    try:
        logging.info('Disabling %s before evacuation' % service.host)
        _host_disable(conn, service)
    except Exception as e:
        logging.error('Failed to disable %s: %s' % (service.host, e))
        return False

    if 'true' in RESERVED_HOSTS.lower():
        try:
            if reserved_hosts:
                # try enabling a compute in the same AZ
                try:
                    service2 = [host for host in reserved_hosts if host.zone == service.zone][0]
                    reserved_hosts.remove(service2)
                    _host_enable(conn, service2, reenable=False)
                    logging.info('Enabled host %s from the reserved pool' % service2.host)
                except:
                    #logging.error('No reserved compute found in the same AZ')
                    logging.warning('No reserved compute found in the same AZ')
                    #return False
            else:
                #for now we don't care if there are no available reserved hosts to enable
                logging.warning('Not enough hosts available from the reserved pool')
                #return True
                #logging.error('Not enough hosts available from the reserved pool')
                #return False
        except Exception as e:
            logging.warning('Failed to enable reserved compute for %s: %s' % (service.host, e))
            logging.debug('Exception traceback:', exc_info=True)
            return False

    try:
        logging.info('Start evacuation of %s' % service.host)
        evacuation_result = _host_evacuate(conn, service.host)
    except Exception as e:
        logging.error('Failed to evacuate %s: %s' % (service.host, e))
        logging.debug('Exception traceback:', exc_info=True)
        return False

    if evacuation_result:
        if 'true' in LEAVE_DISABLED.lower():
            logging.info('Evacuation successful. Not re-enabling %s since LEAVE_DISABLED is set to %s' % (service.host, LEAVE_DISABLED))
            return True
        else:
            logging.info('Evacuation successful. Re-enabling %s' % service.host)

            try:
                _host_fence(service.host, 'on')
            except Exception as e:
                logging.error('Failed to power on %s: %s' % (service.host, e))
                logging.debug('Exception traceback:', exc_info=True)
                return False

            try:
                _host_enable(conn, service, reenable=False)
            except Exception as e:
                logging.error('Failed to enable %s: %s' % (service.host, e))
                logging.debug('Exception traceback:', exc_info=True)
                return False
            return True


def main():

    CLOUD = os.getenv('OS_CLOUD', 'overcloud')

    try:
        username = clouds[CLOUD]["auth"]["username"]
        projectname = clouds[CLOUD]["auth"]["project_name"]
        auth_url = clouds[CLOUD]["auth"]["auth_url"]
        user_domain_name = clouds[CLOUD]["auth"]["user_domain_name"]
        project_domain_name = clouds[CLOUD]["auth"]["project_domain_name"]
        password = secure[CLOUD]["auth"]["password"]
    except Exception as e:
        logging.error("Could not find valid data for Cloud: %s" % CLOUD)
        sys.exit(1)

    try:
        conn = nova_login(username, password, projectname, auth_url, user_domain_name, project_domain_name)

    except Exception as e:
        logging.error("Failed: Unable to connect to Nova: " + str(e))

    while True:
        services = conn.services.list(binary="nova-compute")

        # How fast do we want to react / how much do we want to wait before considering a host worth of our attention
        # We take the current time and subtract a DELTA amount of seconds to have a point in time threshold
        target_date = datetime.now() - timedelta(seconds=DELTA)

        # We check if a host is still up but has not been reporting its state for the last DELTA seconds or if it is down.
        # We filter previously disabled hosts or the ones that are forced_down.
        compute_nodes = [service for service in services if (datetime.fromisoformat(service.updated_at) < target_date and service.state != 'down') or (service.state == 'down') and 'disabled' not in service.status and not service.forced_down]

        if not compute_nodes == []:
            logging.warning('The following computes are down:' + str([service.host for service in compute_nodes]))

            # Filter out computes that have no vms running (we don't want to waste time evacuating those anyway)
            compute_nodes = [service for service in compute_nodes if service not in [c for c in compute_nodes if not conn.servers.list(search_opts={'host': c.host, 'all_tenants': 1})]]

            # Check if there are images, flavors or aggregates configured with the EVACUABLE tag
            images = _get_evacuable_images(conn)
            flavors = _get_evacuable_flavors(conn)
            evacuable_aggregates = [i for i in conn.aggregates.list() if EVACUABLE_TAG in i.metadata]

            if flavors or images:
                compute_nodes = [s for s in compute_nodes if [v for v in conn.servers.list(search_opts={'host': s.host, 'all_tenants': 1 }) if _is_server_evacuable(v, flavors, images)]]

            if evacuable_aggregates:
                # Filter out computes not part of evacuable aggregates (if any aggregate is tagged, otherwise evacuate them all)
                compute_nodes = [service for service in compute_nodes if _is_aggregate_evacuable(conn, service.host)]

            logging.debug('List of stale services is %s' % [service.host for service in compute_nodes])

            # Get list of reserved hosts (if feature is enabled)
            if 'true' in RESERVED_HOSTS.lower():
                reserved_hosts = [service for service in services if ('disabled' in service.status and 'reserved' in service.disabled_reason )]
            else:
                reserved_hosts = []

            if compute_nodes:
                if (len(compute_nodes) / len(services) * 100) > THRESHOLD:
                    logging.error('Number of impacted computes exceeds the defined threshold. There is something wrong.')
                    pass
                else:
                    if 'true' in CHECK_KDUMP.lower():
                        # Check if some of these computes are crashed and currently kdumping
                        to_evacuate = _check_kdump(compute_nodes)
                    else:
                        to_evacuate = compute_nodes

                    if 'false' in DISABLED.lower():
                        with concurrent.futures.ThreadPoolExecutor() as executor:
                            results = list(executor.map(lambda service: process_service(service, reserved_hosts), to_evacuate))
                        if not all(results):
                            logging.warning('Some services failed to evacuate. Retrying in 30 seconds.')

                    else:
                        logging.info('InstanceHA DISABLE is true, not evacuating')

        # We need to wait until a compute is back and for the migrations to move from 'done' to 'completed' before we can force_down=false

        to_reenable = [service for service in services if 'enabled' in service.status and service.forced_down]
        if to_reenable:
            logging.debug('The following computes have forced_down=true, checking if they can be re-enabled: %s' % repr(to_reenable))

            # list all the migrations having each compute as source, if they are all completed go ahead and re-enable it
            for i in to_reenable:
                migr = conn.migrations.list(source_compute=i.host, migration_type='evacuation', limit='100')
                incomplete = [a.id for a in migr if 'completed' not in a.status]
                if incomplete == []:
                    logging.debug('All migrations completed, reenabling %s' % i.host)
                    try:
                        _host_enable(conn, i, reenable=True)
                    except Exception as e:
                        logging.error('Failed to enable %s: %s' % (service.host, e))
                        logging.debug('Exception traceback:', exc_info=True)
                else:
                    logging.debug('At least one migration not completed %s, not reenabling %s' % (incomplete, i.host) )

        time.sleep(POLL)


if __name__ == "__main__":
    main()
