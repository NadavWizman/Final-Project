"""Account roles.

The blockchain nodes authenticate to the API as ordinary Django users that
belong to the ``consensus_nodes`` group. Membership — not ``is_staff`` — is
what allows listing every order and executing/rejecting orders, so admin
staff do not implicitly act as nodes and node accounts get no admin access.
"""

CONSENSUS_NODES_GROUP = 'consensus_nodes'


def is_consensus_node(user):
    if not (user and user.is_authenticated):
        return False
    # cache on the user object: several checks can run in one request
    cached = getattr(user, '_is_consensus_node', None)
    if cached is None:
        cached = user.groups.filter(name=CONSENSUS_NODES_GROUP).exists()
        user._is_consensus_node = cached
    return cached
