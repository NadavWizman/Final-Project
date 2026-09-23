from django.db import migrations

GROUP = 'consensus_nodes'
NODE_USERNAMES = ('node1', 'node2', 'node3')


def forwards(apps, schema_editor):
    Group = apps.get_model('auth', 'Group')
    User = apps.get_model('auth', 'User')
    group, _ = Group.objects.get_or_create(name=GROUP)
    # Existing node accounts were staff users; move them to the node role and
    # drop the admin access they never needed.
    for user in User.objects.filter(username__in=NODE_USERNAMES, is_staff=True, is_superuser=False):
        user.groups.add(group)
        user.is_staff = False
        user.save(update_fields=['is_staff'])


def backwards(apps, schema_editor):
    Group = apps.get_model('auth', 'Group')
    User = apps.get_model('auth', 'User')
    User.objects.filter(groups__name=GROUP).update(is_staff=True)
    Group.objects.filter(name=GROUP).delete()


class Migration(migrations.Migration):

    dependencies = [
        ('auth', '0012_alter_user_first_name_max_length'),
        ('trading', '0014_alter_orderapproval_unique_together_and_more'),
    ]

    operations = [migrations.RunPython(forwards, backwards)]
